package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sitesentry/internal/api"
	"sitesentry/internal/auth"
	"sitesentry/internal/config"
	"sitesentry/internal/detector"
	"sitesentry/internal/llm"
	"sitesentry/internal/mailer"
	"sitesentry/internal/report"
	"sitesentry/internal/scheduler"
	"sitesentry/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("[fatal] %v", err)
	}
	log.SetFlags(log.LstdFlags)
	log.Printf("==============================================")
	log.Printf("🛡️  %s 启动", cfg.AppName)
	log.Printf("==============================================")

	st, err := store.Open(cfg.DSN())
	if err != nil {
		log.Fatalf("[fatal] %v", err)
	}
	if err := st.Migrate(); err != nil {
		log.Fatalf("[fatal] 数据库迁移失败: %v", err)
	}
	// LLM API Key 来自配置文件 llm_api_key 字段或环境变量 SENTINEL_LLM_API_KEY，
	// 首次启动写入默认设置；之后可在「通知与设置」页面随时修改，无需改代码。
	if err := st.Seed(cfg.AppName, cfg.BaseURL, cfg.LLMAPIKey); err != nil {
		log.Printf("[warn] 写入默认设置失败: %v", err)
	}
	if created, err := st.EnsureAdmin("admin", auth.HashPassword("admin123")); err == nil && created {
		log.Printf("[seed] ⚠️  初始管理员已创建 admin / admin123 （请登录后立即修改密码）")
	}

	a := auth.New(st)
	lc := llm.New(st)
	ml := mailer.New(st)
	det := detector.New(st, lc, ml)
	rep := report.New(st, lc, ml)
	h := api.New(cfg, st, a, lc, ml, det, rep)
	router := api.NewRouter(h)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go scheduler.New(cfg, st, det, ml, rep).Run(ctx)

	srv := &http.Server{
		Addr:         cfg.Listen,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		log.Printf("[main] 收到退出信号，正在关闭...")
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()

	log.Printf("[main] HTTP 服务监听 %s", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[fatal] HTTP 服务异常: %v", err)
	}
	_ = os.Stdout
}
