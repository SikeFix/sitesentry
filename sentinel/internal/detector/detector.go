package detector

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"sitesentry/internal/llm"
	"sitesentry/internal/mailer"
	"sitesentry/internal/monitor"
	"sitesentry/internal/store"
	"sitesentry/internal/webhook"
)

type Anomaly struct {
	ID        uint64
	UserID    uint64
	Type      string
	Severity  string
	TargetID  sql.NullInt64
	Source    string
	Title     string
	Detail    string
	LLMText   string
	Status    string
	Notified  int
	CreatedAt time.Time
}

type TargetInfo struct {
	ID            uint64
	UserID        uint64
	Name          string
	URL           string
	NotifyEmails  string
	NotifyRecover int
}

type Detector struct {
	Store *store.Store
	LLM   *llm.Client
	Mail  *mailer.Mailer
}

func New(st *store.Store, lc *llm.Client, ml *mailer.Mailer) *Detector {
	return &Detector{Store: st, LLM: lc, Mail: ml}
}

// RecordCheck 落库探测结果，并用原子条件 UPDATE 判定状态迁移。
// 返回 (becameDown, becameUp)：仅并发探测中真正触发状态翻转的一方会得到 true，
// 从而保证 up→down / down→up 只产生一条异常。
// inMaintenance：维护模式期间只记录探测结果、冻结状态机（不翻转、不告警），
// 维护结束后下一次真实翻转才触发异常。
func (d *Detector) RecordCheck(targetID, userID uint64, r monitor.Result, inMaintenance bool) (bool, bool) {
	now := time.Now()
	okInt := 0
	if r.OK {
		okInt = 1
	}
	errStr := r.Err
	if errStr == "" && r.Note != "" {
		errStr = r.Note
	}
	if len(errStr) > 500 {
		errStr = errStr[:500]
	}
	_, _ = d.Store.DB.Exec(
		`INSERT INTO checks (target_id, user_id, ok, status_code, ms, error, checked_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		targetID, userID, okInt, r.StatusCode, r.MS, errStr, now)

	var certDays interface{}
	if r.CertDays != nil {
		certDays = *r.CertDays
	}
	if inMaintenance {
		// 维护中：仅刷新探测统计，不动状态机
		_, _ = d.Store.DB.Exec(
			`UPDATE monitor_targets SET last_check_at=?, last_ms=?, last_cert_days=COALESCE(?, last_cert_days) WHERE id=?`,
			now, r.MS, certDays, targetID)
		return false, false
	}

	var becameDown, becameUp bool
	if !r.OK {
		res, _ := d.Store.DB.Exec(
			`UPDATE monitor_targets SET status='down', fail_streak=fail_streak+1,
			 last_check_at=?, last_status_code=?, last_ms=?, last_cert_days=COALESCE(?, last_cert_days)
			 WHERE id=? AND status <> 'down'`,
			now, r.StatusCode, r.MS, certDays, targetID)
		if res != nil {
			if n, _ := res.RowsAffected(); n == 1 {
				becameDown = true
			}
		}
	} else {
		res, _ := d.Store.DB.Exec(
			`UPDATE monitor_targets SET status='up', fail_streak=0,
			 last_check_at=?, last_status_code=?, last_ms=?, last_cert_days=COALESCE(?, last_cert_days)
			 WHERE id=? AND status <> 'up'`,
			now, r.StatusCode, r.MS, certDays, targetID)
		if res != nil {
			if n, _ := res.RowsAffected(); n == 1 {
				becameUp = true
			}
		}
	}
	// 未发生翻转时也要刷新统计字段
	if !becameDown && !becameUp {
		_, _ = d.Store.DB.Exec(
			`UPDATE monitor_targets SET last_check_at=?, last_status_code=?, last_ms=?, last_cert_days=COALESCE(?, last_cert_days) WHERE id=?`,
			now, r.StatusCode, r.MS, certDays, targetID)
	}
	return becameDown, becameUp
}

// AfterCheck 根据状态迁移结果评估是否生成异常
func (d *Detector) AfterCheck(t TargetInfo, becameDown, becameUp bool, r monitor.Result) {
	now := time.Now()
	if becameDown {
		detail := fmt.Sprintf("监测目标「%s」（%s）于 %s 探测失败。\n探测错误：%s\nHTTP 状态码：%d，响应耗时：%d ms。",
			t.Name, t.URL, now.Format("2006-01-02 15:04:05"), orDash(r.Err), r.StatusCode, r.MS)
		if r.Note != "" {
			detail += "\n附加信息：" + r.Note
		}
		d.CreateAnomaly(t.UserID, "check_down", "critical", &t.ID, t.Name,
			fmt.Sprintf("网站离线：%s", t.Name), detail, "open")
		return
	}
	if becameUp {
		// 恢复时自动关闭该目标仍处开放状态的离线异常
		if res, err := d.Store.DB.Exec(
			`UPDATE anomalies SET status = 'resolved', resolved_at = NOW()
			 WHERE user_id = ? AND target_id = ? AND type = 'check_down' AND status = 'open'`,
			t.UserID, t.ID); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("[detector] 目标「%s」恢复，自动关闭 %d 条离线异常", t.Name, n)
			}
		}
		if t.NotifyRecover == 1 {
			detail := fmt.Sprintf("监测目标「%s」（%s）已于 %s 恢复正常。\n当前状态码：%d，响应耗时：%d ms。",
				t.Name, t.URL, now.Format("2006-01-02 15:04:05"), r.StatusCode, r.MS)
			// 恢复是信息性事件：创建即自动关闭，不再占用待处理列表
			d.CreateAnomaly(t.UserID, "check_recovery", "warning", &t.ID, t.Name,
				fmt.Sprintf("网站已恢复：%s", t.Name), detail, "resolved")
		}
		return
	}
	// SSL 证书即将到期（仅 HTTPS 在线目标）
	if r.OK && r.CertDays != nil {
		d.evaluateCert(t, *r.CertDays)
	}
	// 响应时间突增（仅在线时评估）
	if r.OK && r.MS > 2000 {
		var avg float64
		if err := d.Store.DB.QueryRow(
			`SELECT COALESCE(AVG(ms),0) FROM checks WHERE target_id = ? AND ok = 1
			 AND checked_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)`, t.ID).Scan(&avg); err == nil && avg >= 300 {
			d.evaluateLatency(t, r, avg)
		}
	}
}

// evaluateCert 证书剩余天数低于阈值时生成告警（已有未关闭的证书告警则不重复）
func (d *Detector) evaluateCert(t TargetInfo, days int) {
	warnDays := d.Store.GetIntSetting("ssl_warn_days", 7)
	if days > warnDays {
		return
	}
	// 24h 内该目标已生成过证书告警则不重复（含已手动关闭的，避免关完立刻再报）
	var n int
	if d.Store.DB.QueryRow(
		`SELECT COUNT(*) FROM anomalies WHERE user_id=? AND type='cert_expiring' AND target_id=?
		 AND created_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)`,
		t.UserID, t.ID).Scan(&n) == nil && n > 0 {
		return
	}
	severity := "warning"
	title := fmt.Sprintf("SSL 证书即将到期：%s（剩余 %d 天）", t.Name, days)
	if days < 0 {
		severity = "critical"
		title = fmt.Sprintf("SSL 证书已过期：%s", t.Name)
	}
	detail := fmt.Sprintf("监测目标「%s」（%s）的 SSL 证书剩余 %d 天（告警阈值 %d 天）。\n请安排续签/更换证书，避免到期后 HTTPS 访问被浏览器拦截。",
		t.Name, t.URL, days, warnDays)
	d.CreateAnomaly(t.UserID, "cert_expiring", severity, &t.ID, t.Name, title, detail, "open")
}

func (d *Detector) evaluateLatency(t TargetInfo, r monitor.Result, avg float64) {
	mult := float64(d.Store.GetIntSetting("latency_multiplier", 3))
	if float64(r.MS) > mult*avg && d.cooldown(t.UserID, "latency_spike", &t.ID, t.Name, 30) {
		detail := fmt.Sprintf("监测目标「%s」（%s）最近一次响应耗时 %d ms，为近 24 小时均值 %.0f ms 的 %.1f 倍，可能存在性能劣化。",
			t.Name, t.URL, r.MS, avg, float64(r.MS)/avg)
		d.CreateAnomaly(t.UserID, "latency_spike", "warning", &t.ID, t.Name,
			fmt.Sprintf("响应变慢：%s（%d ms）", t.Name, r.MS), detail, "open")
	}
}

// cooldown 冷却期内是否已存在同类异常
func (d *Detector) cooldown(userID uint64, typ string, targetID *uint64, source string, minutes int) bool {
	var n int
	if targetID != nil {
		d.Store.DB.QueryRow(
			`SELECT COUNT(*) FROM anomalies WHERE user_id=? AND type=? AND target_id=? AND created_at > DATE_SUB(NOW(), INTERVAL ? MINUTE)`,
			userID, typ, *targetID, minutes).Scan(&n)
	} else {
		d.Store.DB.QueryRow(
			`SELECT COUNT(*) FROM anomalies WHERE user_id=? AND type=? AND source=? AND created_at > DATE_SUB(NOW(), INTERVAL ? MINUTE)`,
			userID, typ, source, minutes).Scan(&n)
	}
	return n == 0
}

// CheckLogBursts 扫描各来源最近 10 分钟的错误日志爆发
func (d *Detector) CheckLogBursts() {
	threshold := d.Store.GetIntSetting("log_burst_threshold", 10)
	rows, err := d.Store.DB.Query(
		`SELECT user_id, source, COUNT(*) AS cnt FROM logs
		 WHERE level IN ('error','fatal') AND created_at > DATE_SUB(NOW(), INTERVAL 10 MINUTE)
		 GROUP BY user_id, source HAVING cnt > ?`, threshold)
	if err != nil {
		return
	}
	type burst struct {
		userID uint64
		source string
		cnt    int
	}
	var bursts []burst
	for rows.Next() {
		var b burst
		if rows.Scan(&b.userID, &b.source, &b.cnt) == nil {
			bursts = append(bursts, b)
		}
	}
	rows.Close()
	for _, b := range bursts {
		if !d.cooldown(b.userID, "log_burst", nil, b.source, 30) {
			continue
		}
		severity := "warning"
		if b.cnt > threshold*3 {
			severity = "critical"
		}
		detail := fmt.Sprintf("来源「%s」在最近 10 分钟内上报了 %d 条 error/fatal 级日志（阈值 %d 条），可能存在服务异常。",
			b.source, b.cnt, threshold)
		d.CreateAnomaly(b.userID, "log_burst", severity, nil, b.source,
			fmt.Sprintf("日志错误爆发：%s（%d 条/10 分钟）", b.source, b.cnt), detail, "open")
	}
}

// CreateAnomaly 插入一条异常（未通知状态）。
// initialStatus：'open' 需人工/AI 处理；'resolved' 创建即关闭（如恢复通知这类信息性事件）。
func (d *Detector) CreateAnomaly(userID uint64, typ, severity string, targetID *uint64, source, title, detail, initialStatus string) {
	var tid sql.NullInt64
	if targetID != nil {
		tid = sql.NullInt64{Int64: int64(*targetID), Valid: true}
	}
	if initialStatus == "" {
		initialStatus = "open"
	}
	// 同一目标的离线异常在未恢复前不重复创建（兜底防并发/重复触发）
	if typ == "check_down" && targetID != nil {
		var n int
		if d.Store.DB.QueryRow(
			`SELECT COUNT(*) FROM anomalies WHERE user_id=? AND type='check_down' AND target_id=? AND status='open'`,
			userID, *targetID).Scan(&n) == nil && n > 0 {
			return
		}
	}
	var resolvedAt interface{}
	if initialStatus == "resolved" {
		resolvedAt = time.Now()
	}
	res, err := d.Store.DB.Exec(
		`INSERT INTO anomalies (user_id, type, severity, target_id, source, title, detail, status, resolved_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, typ, severity, tid, source, title, detail, initialStatus, resolvedAt)
	if err != nil {
		log.Printf("[detector] 创建异常失败: %v", err)
		return
	}
	id, _ := res.LastInsertId()
	log.Printf("[detector] 新异常 #%d [%s/%s] %s (status=%s)", id, typ, severity, title, initialStatus)
}

// ProcessPending 处理尚未通知的新异常：LLM 诊断 + 邮件
func (d *Detector) ProcessPending(limit int) {
	// 恢复类事件创建即为 resolved（无需处理），但仍需走一次通知流程（发恢复邮件），
	// 所以待处理条件为：未通知 且（开放中 或 恢复事件）
	rows, err := d.Store.DB.Query(
		`SELECT id, user_id, type, severity, target_id, source, title, detail
		 FROM anomalies WHERE notified = 0 AND (status = 'open' OR type = 'check_recovery')
		 ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return
	}
	var list []Anomaly
	for rows.Next() {
		var a Anomaly
		if rows.Scan(&a.ID, &a.UserID, &a.Type, &a.Severity, &a.TargetID, &a.Source, &a.Title, &a.Detail) == nil {
			list = append(list, a)
		}
	}
	rows.Close()
	for i := range list {
		// 原子认领：调度器与手动检查接口可能并发调用本函数，
		// 先抢占 notified 标志，抢到的才处理，避免同一异常被重复诊断/重复发邮件
		res, err := d.Store.DB.Exec(
			`UPDATE anomalies SET notified = 1 WHERE id = ? AND notified = 0`, list[i].ID)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		d.processOne(&list[i])
	}
}

func (d *Detector) processOne(a *Anomaly) {
	appName := d.Store.GetSetting("app_name", "SiteSentry 哨兵")
	baseURL := d.Store.GetSetting("base_url", "")

	// 1) LLM 诊断（恢复类跳过，节省调用）；成功后由 AI 给出自动决策（auto_resolve/watch/manual）
	if a.Type != "check_recovery" && d.LLM.Config().Enabled {
		analysis, err := d.diagnose(a)
		if err != nil {
			log.Printf("[detector] 异常 #%d LLM 诊断失败: %v", a.ID, err)
			analysis = fmt.Sprintf("（AI 诊断暂不可用：%s）", err.Error())
			_, _ = d.Store.DB.Exec(
				`UPDATE anomalies SET llm_analysis = ?, llm_at = NOW() WHERE id = ?`, analysis, a.ID)
			a.LLMText = analysis
		} else {
			a.LLMText = d.applyDecision(a, analysis)
		}
	}

	// 2) 邮件通知
	recipients := d.recipients(a)
	if len(recipients) == 0 {
		log.Printf("[detector] 异常 #%d 无可用收件邮箱，跳过邮件", a.ID)
	} else {
		targetURL := ""
		if a.TargetID.Valid {
			var u string
			if err := d.Store.DB.QueryRow(`SELECT url FROM monitor_targets WHERE id = ?`, a.TargetID.Int64).Scan(&u); err == nil {
				targetURL = u
			}
		}
		html, plain := mailer.AlertEmail(appName, baseURL, a.Type, a.Severity, a.Title, a.Detail, a.LLMText, targetURL)
		subject := fmt.Sprintf("[%s] %s", severityLabel(a.Severity), a.Title)
		for _, to := range recipients {
			if err := d.Mail.EnqueueText(a.UserID, to, subject, html, plain); err != nil {
				log.Printf("[detector] 异常 #%d 入邮件队列失败(to=%s): %v", a.ID, to, err)
			}
		}
	}
	// 3) Webhook 通知（飞书/钉钉/企微群机器人，best effort 不阻塞主流程）
	whURL := strings.TrimSpace(d.Store.GetSetting("webhook_url", ""))
	if whURL != "" {
		whType := d.Store.GetSetting("webhook_type", "feishu")
		subject := fmt.Sprintf("[%s] %s", severityLabel(a.Severity), a.Title)
		var b strings.Builder
		b.WriteString("时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
		b.WriteString("类型: " + typeLabelCN(a.Type) + "\n")
		if a.Detail != "" {
			b.WriteString("详情: " + fold(a.Detail, 300) + "\n")
		}
		if a.LLMText != "" {
			b.WriteString("AI 诊断摘要: " + fold(firstLine(a.LLMText), 200))
		}
		go func() {
			if err := webhook.Send(whURL, whType, subject, b.String()); err != nil {
				log.Printf("[detector] 异常 #%d webhook 发送失败: %v", a.ID, err)
			}
		}()
	}
}

func typeLabelCN(t string) string {
	switch t {
	case "check_down":
		return "网站离线"
	case "check_recovery":
		return "网站恢复"
	case "latency_spike":
		return "响应变慢"
	case "log_burst":
		return "日志爆发"
	case "external":
		return "外部上报"
	case "cert_expiring":
		return "证书到期"
	}
	return t
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}

func fold(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// diagnose 组装上下文并调用 LLM
func (d *Detector) diagnose(a *Anomaly) (string, error) {
	var sb strings.Builder
	// 目标信息 + 最近探测
	if a.TargetID.Valid {
		var name, url string
		if d.Store.DB.QueryRow(`SELECT name, url FROM monitor_targets WHERE id = ?`, a.TargetID.Int64).Scan(&name, &url) == nil {
			sb.WriteString(fmt.Sprintf("### 监测目标\n名称: %s\nURL: %s\n\n### 最近 10 次探测记录\n", name, url))
			rows, err := d.Store.DB.Query(
				`SELECT checked_at, ok, status_code, ms, error FROM checks
				 WHERE target_id = ? ORDER BY id DESC LIMIT 10`, a.TargetID.Int64)
			if err == nil {
				defer rows.Close()
				sb.WriteString("时间 | 状态 | HTTP码 | 耗时ms | 错误\n")
				for rows.Next() {
					var (
						t     time.Time
						ok    int
						code  int
						ms    int
						errStr string
					)
					if rows.Scan(&t, &ok, &code, &ms, &errStr) == nil {
						st := "正常"
						if ok == 0 {
							st = "失败"
						}
						sb.WriteString(fmt.Sprintf("%s | %s | %d | %d | %s\n",
							t.Format("01-02 15:04:05"), st, code, ms, orDash(errStr)))
					}
				}
				sb.WriteString("\n")
			}
		}
	}
	// 最近错误日志
	sb.WriteString("### 最近 20 条 error/fatal 日志\n")
	rows, err := d.Store.DB.Query(
		`SELECT created_at, source, level, message FROM logs
		 WHERE user_id = ? AND level IN ('error','fatal')
		 `+extraLogFilter(a)+` ORDER BY id DESC LIMIT 20`, a.UserID)
	if err == nil {
		defer rows.Close()
		n := 0
		for rows.Next() {
			var (
				t      time.Time
				src    string
				level  string
				msg    string
			)
			if rows.Scan(&t, &src, &level, &msg) == nil {
				if len(msg) > 300 {
					msg = msg[:300] + "..."
				}
				sb.WriteString(fmt.Sprintf("[%s] [%s][%s] %s\n", t.Format("01-02 15:04:05"), src, level, msg))
				n++
			}
		}
		if n == 0 {
			sb.WriteString("（无）\n")
		}
	}
	return d.LLM.Diagnose(a.Type, a.Severity, a.Title, a.Detail, sb.String())
}

// applyDecision 保存 LLM 分析并解析其自动决策。
// 决策为 auto_resolve 且开关开启、且目标当前确实已恢复（或本就无目标可查）时，
// 自动关闭该异常，返回去掉 DECISION 行后的展示文本。
func (d *Detector) applyDecision(a *Anomaly, analysis string) string {
	decision := llm.ParseDecision(analysis)
	display := llm.StripDecision(analysis)
	_, _ = d.Store.DB.Exec(
		`UPDATE anomalies SET llm_analysis = ?, llm_at = NOW(), ai_decision = ? WHERE id = ?`,
		display, decision, a.ID)
	if decision == "auto_resolve" && d.Store.GetSetting("ai_auto_resolve", "1") == "1" {
		// 离线类异常仅当目标当前确实在线才允许自动关闭，防止误关仍在故障的站点
		if a.Type != "check_down" || d.targetCurrentlyUp(a.TargetID) {
			if res, err := d.Store.DB.Exec(
				`UPDATE anomalies SET status = 'resolved', resolved_at = NOW() WHERE id = ? AND status = 'open'`,
				a.ID); err == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					log.Printf("[detector] 异常 #%d 由 AI 决策自动解决（auto_resolve）", a.ID)
				}
			}
		} else {
			log.Printf("[detector] 异常 #%d AI 建议 auto_resolve 但目标当前仍离线，保持待处理", a.ID)
		}
	}
	return display
}

// targetCurrentlyUp 目标当前状态是否为 up
func (d *Detector) targetCurrentlyUp(targetID sql.NullInt64) bool {
	if !targetID.Valid {
		return false
	}
	var st string
	if d.Store.DB.QueryRow(`SELECT status FROM monitor_targets WHERE id = ?`, targetID.Int64).Scan(&st) == nil {
		return st == "up"
	}
	return false
}

// extraLogFilter 针对特定异常的日志过滤
func extraLogFilter(a *Anomaly) string {
	switch a.Type {
	case "log_burst":
		return fmt.Sprintf("AND source = %s ", quoteStr(a.Source))
	case "check_down", "check_recovery", "latency_spike":
		return fmt.Sprintf("AND (source = %s OR source LIKE %s) ",
			quoteStr(a.Source), quoteStr(a.Source+"%"))
	}
	return ""
}

func quoteStr(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `'`, `\'`) + "'"
}

// recipients 计算告警收件人：优先目标级邮箱，否则全局默认
func (d *Detector) recipients(a *Anomaly) []string {
	if a.TargetID.Valid {
		var emails string
		if d.Store.DB.QueryRow(`SELECT notify_emails FROM monitor_targets WHERE id = ?`, a.TargetID.Int64).Scan(&emails) == nil {
			if list := cleanEmails(emails); len(list) > 0 {
				return list
			}
		}
	}
	if list := cleanEmails(d.Store.GetSetting("default_notify_emails", "")); len(list) > 0 {
		return list
	}
	return nil
}

func cleanEmails(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		e := strings.TrimSpace(p)
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

func severityLabel(s string) string {
	if s == "critical" {
		return "严重"
	}
	return "警告"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Rediagnose 手动重新诊断某条异常
func (d *Detector) Rediagnose(id, userID uint64) (string, error) {
	var a Anomaly
	err := d.Store.DB.QueryRow(
		`SELECT id, user_id, type, severity, target_id, source, title, detail
		 FROM anomalies WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&a.ID, &a.UserID, &a.Type, &a.Severity, &a.TargetID, &a.Source, &a.Title, &a.Detail)
	if err != nil {
		return "", fmt.Errorf("异常不存在")
	}
	analysis, err := d.diagnose(&a)
	if err != nil {
		return "", err
	}
	return d.applyDecision(&a, analysis), nil
}

// DiagContext 供 AI 聊天使用：用户数据摘要
func (d *Detector) UserDigest(userID uint64) string {
	var sb strings.Builder
	// 监测目标实时状态
	rowsT, errT := d.Store.DB.Query(
		`SELECT t.name, t.url, t.status, t.last_check_at, t.last_ms, t.fail_streak,
		        (SELECT ROUND(100.0*SUM(k.ok)/COUNT(*),1) FROM checks k
		         WHERE k.target_id = t.id AND k.checked_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)) AS up24
		 FROM monitor_targets t WHERE t.user_id = ? ORDER BY t.id LIMIT 20`, userID)
	if errT == nil {
		defer rowsT.Close()
		sb.WriteString("## 监测目标实时状态\n")
		n := 0
		for rowsT.Next() {
			var (
				name, urlStr, status string
				lastAt               sql.NullTime
				lastMS               sql.NullInt64
				streak               int
				up24                 interface{}
			)
			if rowsT.Scan(&name, &urlStr, &status, &lastAt, &lastMS, &streak, &up24) == nil {
				line := fmt.Sprintf("- %s（%s）：状态=%s", name, urlStr, status)
				if lastAt.Valid {
					line += fmt.Sprintf("，最近探测 %s", lastAt.Time.Format("01-02 15:04:05"))
				}
				if lastMS.Valid {
					line += fmt.Sprintf("（%d ms）", lastMS.Int64)
				}
				if s, ok := up24.([]byte); ok && len(s) > 0 {
					line += fmt.Sprintf("，24h 可用率 %s%%", s)
				}
				if f, ok := up24.(float64); ok {
					line += fmt.Sprintf("，24h 可用率 %.1f%%", f)
				}
				line += fmt.Sprintf("，连续失败 %d 次", streak)
				sb.WriteString(line + "\n")
				n++
			}
		}
		if n == 0 {
			sb.WriteString("（无监测目标）\n")
		}
		sb.WriteString("\n")
	}
	// 未处理异常
	rows, err := d.Store.DB.Query(
		`SELECT created_at, type, severity, title, detail FROM anomalies
		 WHERE user_id = ? AND status = 'open' ORDER BY id DESC LIMIT 5`, userID)
	if err == nil {
		defer rows.Close()
		sb.WriteString("## 当前未处理异常\n")
		n := 0
		for rows.Next() {
			var (
				t      time.Time
				typ    string
				sev    string
				title  string
				detail string
			)
			if rows.Scan(&t, &typ, &sev, &title, &detail) == nil {
				if len(detail) > 200 {
					detail = detail[:200] + "..."
				}
				sb.WriteString(fmt.Sprintf("- [%s][%s] %s: %s\n", t.Format("01-02 15:04:05"), sev, title, detail))
				n++
			}
		}
		if n == 0 {
			sb.WriteString("（无）\n")
		}
	}
	// 最近错误日志
	rows2, err2 := d.Store.DB.Query(
		`SELECT created_at, source, level, message FROM logs
		 WHERE user_id = ? AND level IN ('error','fatal') ORDER BY id DESC LIMIT 20`, userID)
	if err2 == nil {
		defer rows2.Close()
		sb.WriteString("\n## 最近 error/fatal 日志\n")
		n := 0
		for rows2.Next() {
			var (
				t     time.Time
				src   string
				level string
				msg   string
			)
			if rows2.Scan(&t, &src, &level, &msg) == nil {
				if len(msg) > 200 {
					msg = msg[:200] + "..."
				}
				sb.WriteString(fmt.Sprintf("- [%s][%s][%s] %s\n", t.Format("01-02 15:04:05"), src, level, msg))
				n++
			}
		}
		if n == 0 {
			sb.WriteString("（无）\n")
		}
	}
	return sb.String()
}

// 保留 context 供未来扩展
var _ = context.Background
