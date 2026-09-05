package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"sitesentry/internal/config"
	"sitesentry/internal/detector"
	"sitesentry/internal/mailer"
	"sitesentry/internal/monitor"
	"sitesentry/internal/store"
)

type Scheduler struct {
	Cfg *config.Config
	St  *store.Store
	Det *detector.Detector
	Mail *mailer.Mailer
	mu  sync.Mutex
}

func New(cfg *config.Config, st *store.Store, det *detector.Detector, mail *mailer.Mailer) *Scheduler {
	return &Scheduler{Cfg: cfg, St: st, Det: det, Mail: mail}
}

// Run 启动周期调度（进程内定时器，每 tickSec 一轮）
func (s *Scheduler) Run(ctx context.Context) {
	interval := time.Duration(s.Cfg.CheckTickSec) * time.Second
	log.Printf("[scheduler] 启动，周期 %v", interval)
	// 启动后立即执行一轮
	s.safeTick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[scheduler] 退出")
			return
		case <-ticker.C:
			s.safeTick()
		}
	}
}

func (s *Scheduler) safeTick() {
	if !s.mu.TryLock() {
		log.Printf("[scheduler] 上一轮未结束，跳过本轮")
		return
	}
	defer s.mu.Unlock()
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[scheduler] panic: %v", r)
		}
	}()
	n1 := s.checkDueTargets()
	s.Det.CheckLogBursts()
	s.Det.ProcessPending(5)
	n2 := s.Mail.FlushQueue(20)
	s.maybeCleanup()
	if n1 > 0 || n2 > 0 {
		log.Printf("[scheduler] 本轮完成: 探测 %d 个目标, 发送 %d 封邮件, 耗时 %v", n1, n2, time.Since(start).Round(time.Millisecond))
	}
}

// checkDueTargets 探测所有到期目标
func (s *Scheduler) checkDueTargets() int {
	rows, err := s.St.DB.Query(
		`SELECT id, user_id, name, url, target_type, expect_status, keyword, interval_sec, timeout_sec,
		        notify_emails, notify_recovery, status,
		        (maintenance_until IS NOT NULL AND maintenance_until > NOW()) AS in_maint
		 FROM monitor_targets
		 WHERE enabled = 1 AND (last_check_at IS NULL OR last_check_at <= DATE_SUB(NOW(), INTERVAL interval_sec SECOND))
		 ORDER BY last_check_at ASC LIMIT 50`)
	if err != nil {
		log.Printf("[scheduler] 查询到期目标失败: %v", err)
		return 0
	}
	type due struct {
		id            uint64
		userID        uint64
		name          string
		url           string
		targetType    string
		expectStatus  int
		keyword       string
		intervalSec   int
		timeoutSec    int
		notifyEmails  string
		notifyRecover int
		status        string
		inMaint       bool
	}
	var list []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.id, &d.userID, &d.name, &d.url, &d.targetType, &d.expectStatus, &d.keyword,
			&d.intervalSec, &d.timeoutSec, &d.notifyEmails, &d.notifyRecover, &d.status, &d.inMaint) == nil {
			list = append(list, d)
		}
	}
	rows.Close()

	count := 0
	for _, d := range list {
		var r monitor.Result
		if d.targetType == "tcp" {
			r = monitor.ProbeTCP(d.url, d.timeoutSec)
		} else {
			r = monitor.Probe(d.url, d.timeoutSec, d.expectStatus, d.keyword)
		}
		becameDown, becameUp := s.Det.RecordCheck(d.id, d.userID, r, d.inMaint)
		// 维护模式：只记录探测结果，不评估异常（状态机已冻结）
		if !d.inMaint {
			s.Det.AfterCheck(detector.TargetInfo{
				ID: d.id, UserID: d.userID, Name: d.name, URL: d.url,
				NotifyEmails: d.notifyEmails, NotifyRecover: d.notifyRecover,
			}, becameDown, becameUp, r)
		}
		count++
		if !r.OK && !d.inMaint {
			log.Printf("[scheduler] 目标 %d(%s) 探测失败: %s", d.id, d.name, r.Err)
		}
	}
	return count
}

// maybeCleanup 每小时做一次数据清理
var lastCleanup time.Time

func (s *Scheduler) maybeCleanup() {
	now := time.Now()
	if now.Sub(lastCleanup) < time.Hour {
		return
	}
	lastCleanup = now
	go func() {
		if res, err := s.St.DB.Exec(`DELETE FROM checks WHERE checked_at < DATE_SUB(NOW(), INTERVAL 30 DAY)`); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("[cleanup] 清理 30 天前探测记录 %d 条", n)
			}
		}
		if res, err := s.St.DB.Exec(`DELETE FROM logs WHERE created_at < DATE_SUB(NOW(), INTERVAL 30 DAY)`); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("[cleanup] 清理 30 天前日志 %d 条", n)
			}
		}
		if res, err := s.St.DB.Exec(`DELETE FROM mail_log WHERE sent_at < DATE_SUB(NOW(), INTERVAL 7 DAY)`); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("[cleanup] 清理 7 天前邮件日志 %d 条", n)
			}
		}
		if res, err := s.St.DB.Exec(`DELETE FROM sessions WHERE expires_at < NOW()`); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("[cleanup] 清理过期会话 %d 个", n)
			}
		}
	}()
}
