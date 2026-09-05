package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
	"sitesentry/internal/config"
	"sitesentry/internal/detector"
	"sitesentry/internal/llm"
	"sitesentry/internal/mailer"
	"sitesentry/internal/store"
)

type Handler struct {
	Cfg *config.Config
	St  *store.Store
	A   *auth.Auth
	LLM *llm.Client
	Mail *mailer.Mailer
	Det *detector.Detector
}

func New(cfg *config.Config, st *store.Store, a *auth.Auth, lc *llm.Client, ml *mailer.Mailer, dt *detector.Detector) *Handler {
	return &Handler{Cfg: cfg, St: st, A: a, LLM: lc, Mail: ml, Det: dt}
}

// ok / fail 统一响应
func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
}

func fail(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"ok": false, "error": msg})
}

func badReq(c *gin.Context, msg string) {
	fail(c, http.StatusBadRequest, msg)
}

func notFound(c *gin.Context, msg string) {
	fail(c, http.StatusNotFound, msg)
}

// parseJSON 读取 JSON body（限制 2MB）
func parseJSON(c *gin.Context, v any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	return c.ShouldBindJSON(v)
}

func NewRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	s := r.Group("/api")
	{
		s.POST("/auth/register", h.Register)
		s.POST("/auth/login", h.Login)
		s.POST("/auth/logout", h.Logout)
		s.GET("/auth/me", h.A.Middleware(), h.Me)

		// 公开状态页数据（无需登录，供 /status 页面使用）
		s.GET("/public/status", h.PublicStatus)
		s.GET("/public/targets/:id", h.PublicTarget)
		s.GET("/public/icon", h.PublicIcon)

		g := s.Group("", h.A.Middleware())
		{
			g.POST("/auth/password", h.ChangePassword)
			g.GET("/dashboard", h.Dashboard)

			g.GET("/targets", h.ListTargets)
			g.POST("/targets", h.CreateTarget)
			g.PUT("/targets/:id", h.UpdateTarget)
			g.DELETE("/targets/:id", h.DeleteTarget)
			g.POST("/targets/:id/check", h.CheckNow)
			g.GET("/targets/:id/history", h.TargetHistory)

			g.GET("/logs", h.ListLogs)
			g.GET("/logs/sources", h.LogSources)

			g.GET("/anomalies", h.ListAnomalies)
			g.GET("/anomalies/:id", h.GetAnomaly)
			g.POST("/anomalies/:id/resolve", h.ResolveAnomaly)
			g.POST("/anomalies/:id/rediagnose", h.Reddiagnose)

			g.GET("/ai/conversations", h.ListConversations)
			g.POST("/ai/conversations", h.CreateConversation)
			g.DELETE("/ai/conversations/:id", h.DeleteConversation)
			g.GET("/ai/conversations/:id/messages", h.ConversationMessages)
			g.POST("/ai/conversations/:id/messages", h.SendMessage)
			g.POST("/ai/conversations/:id/messages/stream", h.StreamMessage)

			g.GET("/tokens", h.ListTokens)
			g.POST("/tokens", h.CreateToken)
			g.DELETE("/tokens/:id", h.RevokeToken)

			g.GET("/settings", h.GetSettings)
			g.POST("/settings", auth.AdminOnly(), h.SaveSettings)
			g.POST("/settings/test-mail", h.TestMail)
			g.POST("/settings/test-llm", h.TestLLM)
			g.POST("/settings/test-webhook", h.TestWebhook)

			g.GET("/users", auth.AdminOnly(), h.ListUsers)
			g.POST("/users", auth.AdminOnly(), h.CreateUser)
			g.PUT("/users/:id", auth.AdminOnly(), h.UpdateUser)
			g.DELETE("/users/:id", auth.AdminOnly(), h.DeleteUser)
		}

		v1 := s.Group("/v1", h.CORSMiddleware(), h.TokenAuth)
		{
			v1.POST("/logs", h.ReportLog)
			v1.POST("/anomalies", h.ReportAnomaly)
			v1.GET("/ping", h.PingToken)
		}
	}

	// 静态资源 + SPA 回退
	r.NoRoute(h.StaticFallback)
	return r
}

// CORSMiddleware 允许外部网站跨域调用上报接口
func (h *Handler) CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Token")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// StaticFallback 提供前端静态文件，未匹配路径回退 index.html
// 静态资源一律 no-cache：每次加载向服务器校验（配合 Last-Modified 返回 304），
// 保证前端发版后用户刷新即见新版，不会被浏览器启发式缓存挡住。
func (h *Handler) StaticFallback(c *gin.Context) {
	p := c.Request.URL.Path
	if strings.HasPrefix(p, "/api/") {
		fail(c, http.StatusNotFound, "接口不存在")
		return
	}
	c.Header("Cache-Control", "no-cache")
	rel := filepath.Clean(strings.TrimPrefix(p, "/"))
	// 首页 = 公开状态页；/admin（含子路径）= 管理端 SPA；/status 兼容旧链接
	switch {
	case rel == ".":
		rel = "status.html"
	case rel == "admin" || strings.HasPrefix(rel, "admin/"):
		rel = "index.html"
	case rel == "status":
		if fi, err := os.Stat(filepath.Join(h.Cfg.WebDir, "status.html")); err == nil && !fi.IsDir() {
			c.File(filepath.Join(h.Cfg.WebDir, "status.html"))
			return
		}
		rel = "status.html"
	}
	fpath := filepath.Join(h.Cfg.WebDir, rel)
	if fi, err := os.Stat(fpath); err == nil && !fi.IsDir() {
		c.File(fpath)
		return
	}
	// 未知路径统一回落到公开状态页
	c.File(filepath.Join(h.Cfg.WebDir, "status.html"))
}

// idParam 从路径取 :id
func idParam(c *gin.Context) (uint64, bool) {
	return parseID(c.Param("id"))
}

func parseID(s string) (uint64, bool) {
	var n uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + uint64(ch-'0')
	}
	return n, n > 0
}

var _ = io.Discard
