// Package report 智能报告：按日/周窗口汇总探测、异常、日志数据，
// 调用 LLM 生成中文 Markdown 运营报告（AI 未启用时退化为规则引擎直出），
// 可选邮件推送，报告持久化到 ai_reports 表供后台查看。
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"sitesentry/internal/llm"
	"sitesentry/internal/mailer"
	"sitesentry/internal/store"
)

type Reporter struct {
	Store *store.Store
	LLM   *llm.Client
	Mail  *mailer.Mailer
}

func New(st *store.Store, lc *llm.Client, ml *mailer.Mailer) *Reporter {
	return &Reporter{Store: st, LLM: lc, Mail: ml}
}

func kindLabel(kind string) string {
	if kind == "weekly" {
		return "监测周报"
	}
	return "监测日报"
}

func kindHours(kind string) int {
	if kind == "weekly" {
		return 24 * 7
	}
	return 24
}

// Create 插入一条 pending 报告并异步生成，立即返回报告 ID（前端轮询状态）
func (r *Reporter) Create(userID uint64, kind string, wantEmail bool) (uint64, error) {
	if kind != "weekly" {
		kind = "daily"
	}
	now := time.Now()
	title := fmt.Sprintf("%s · %s", kindLabel(kind), now.Format("2006-01-02"))
	if kind == "weekly" {
		title = fmt.Sprintf("%s · %s ~ %s", kindLabel(kind),
			now.AddDate(0, 0, -6).Format("01-02"), now.Format("01-02"))
	}
	we := 0
	if wantEmail {
		we = 1
	}
	res, err := r.Store.DB.Exec(
		`INSERT INTO ai_reports (user_id, kind, title, content, want_email) VALUES (?, ?, ?, '', ?)`,
		userID, kind, title, we)
	if err != nil {
		return 0, fmt.Errorf("创建报告任务失败: %w", err)
	}
	insID, _ := res.LastInsertId()
	id := uint64(insID)
	go r.run(id)
	return id, nil
}

func (r *Reporter) run(id uint64) {
	if err := r.generate(id); err != nil {
		log.Printf("[report] 报告 #%d 生成失败: %v", id, err)
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_, _ = r.Store.DB.Exec(
			`UPDATE ai_reports SET status='failed', error=? WHERE id=?`, msg, id)
	}
}

type targetStat struct {
	Name    string  `json:"name"`
	URL     string  `json:"url"`
	Status  string  `json:"status"`
	Checks  int     `json:"checks"`
	Uptime  float64 `json:"uptime"`
	AvgMS   float64 `json:"avg_ms"`
}

type metrics struct {
	WindowFrom   string       `json:"window_from"`
	WindowTo     string       `json:"window_to"`
	Targets      []targetStat `json:"targets"`
	AnomTotal    int          `json:"anomalies_total"`
	AnomOpen     int          `json:"anomalies_open"`
	LogByLevel   map[string]int `json:"log_by_level"`
	TopErrSource map[string]int `json:"top_error_sources"`
	CertRisks    []string     `json:"cert_risks"`
}

// generate 收集数据 → LLM（或规则兜底）→ 落库 → 可选发邮件
func (r *Reporter) generate(id uint64) error {
	var (
		userID uint64
		kind   string
	)
	if err := r.Store.DB.QueryRow(`SELECT user_id, kind FROM ai_reports WHERE id = ?`, id).Scan(&userID, &kind); err != nil {
		return fmt.Errorf("报告不存在: %w", err)
	}
	hours := kindHours(kind)
	now := time.Now()
	from := now.Add(-time.Duration(hours) * time.Hour)

	m, contextBlock, err := r.collect(userID, from, now)
	if err != nil {
		return err
	}
	metricsJSON, _ := json.Marshal(m)

	content := ""
	if r.LLM.Config().Enabled {
		text, err := r.LLM.Report(contextBlock)
		if err != nil {
			log.Printf("[report] 报告 #%d LLM 生成失败，改用规则引擎兜底: %v", id, err)
		} else {
			content = text
		}
	}
	if content == "" {
		content = fallbackReport(kind, m, from, now)
	}

	if _, err := r.Store.DB.Exec(
		`UPDATE ai_reports SET status='done', content=?, metrics=? WHERE id=?`,
		content, metricsJSON, id); err != nil {
		return fmt.Errorf("保存报告失败: %w", err)
	}
	log.Printf("[report] 报告 #%d 生成完成（%s，%d 字）", id, kind, len(content))

	// 需要邮件但尚未发送过 → 发送
	var wantEmail, sent int
	if err := r.Store.DB.QueryRow(`SELECT want_email, sent FROM ai_reports WHERE id = ?`, id).
		Scan(&wantEmail, &sent); err == nil && wantEmail == 1 && sent == 0 {
		if n, err := r.SendEmail(id, userID); err != nil {
			log.Printf("[report] 报告 #%d 邮件发送失败: %v", id, err)
		} else if n > 0 {
			_, _ = r.Store.DB.Exec(`UPDATE ai_reports SET sent=1 WHERE id=?`, id)
		}
	}
	return nil
}

// SendEmail 将某份已完成报告邮件推送给默认通知邮箱，返回实际入件数
func (r *Reporter) SendEmail(id, userID uint64) (int, error) {
	var (
		kind, title, content string
		status               string
	)
	err := r.Store.DB.QueryRow(
		`SELECT kind, title, content, status FROM ai_reports WHERE id = ? AND user_id = ?`, id, userID,
	).Scan(&kind, &title, &content, &status)
	if err != nil {
		return 0, fmt.Errorf("报告不存在")
	}
	if status != "done" {
		return 0, fmt.Errorf("报告尚未生成完成")
	}
	recipients := cleanEmails(r.Store.GetSetting("default_notify_emails", ""))
	if len(recipients) == 0 {
		return 0, fmt.Errorf("未配置默认通知邮箱（设置 → 通知邮箱）")
	}
	appName := r.Store.GetSetting("app_name", "SiteSentry 哨兵")
	baseURL := r.Store.GetSetting("base_url", "")
	subject := fmt.Sprintf("[%s] %s", appName, title)
	html, plain := mailer.ReportEmail(appName, baseURL, kindLabel(kind), title, content)
	n := 0
	for _, to := range recipients {
		if err := r.Mail.EnqueueText(userID, to, subject, html, plain); err != nil {
			return n, fmt.Errorf("入邮件队列失败: %w", err)
		}
		n++
	}
	return n, nil
}

// collect 汇总窗口内的监测/异常/日志数据，同时产出给 LLM 的上下文文本
func (r *Reporter) collect(userID uint64, from, to time.Time) (*metrics, string, error) {
	m := &metrics{
		WindowFrom:   from.Format("2006-01-02 15:04"),
		WindowTo:     to.Format("2006-01-02 15:04"),
		LogByLevel:   map[string]int{},
		TopErrSource: map[string]int{},
	}

	// 1) 各目标统计
	rows, err := r.Store.DB.Query(
		`SELECT t.name, t.url, t.status,
		        COUNT(k.id) AS cnt,
		        COALESCE(SUM(k.ok),0) AS okc,
		        COALESCE(ROUND(100.0*SUM(k.ok)/NULLIF(COUNT(k.id),0),1),0) AS uptime,
		        COALESCE(ROUND(AVG(k.ms)),0) AS avgms
		 FROM monitor_targets t
		 LEFT JOIN checks k ON k.target_id = t.id AND k.checked_at BETWEEN ? AND ?
		 WHERE t.user_id = ?
		 GROUP BY t.id, t.name, t.url, t.status
		 ORDER BY t.id`, from, to, userID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("统计窗口: %s ~ %s（%d 小时）\n\n## 监测目标\n", m.WindowFrom, m.WindowTo, int(to.Sub(from).Hours()/3600)))
	for rows.Next() {
		var ts targetStat
		var okc int
		if rows.Scan(&ts.Name, &ts.URL, &ts.Status, &ts.Checks, &okc, &ts.Uptime, &ts.AvgMS) == nil {
			m.Targets = append(m.Targets, ts)
			failN := ts.Checks - okc
			st := map[string]string{"up": "在线", "down": "离线", "unknown": "未检查"}[ts.Status]
			sb.WriteString(fmt.Sprintf("- %s（%s）：当前%s，探测 %d 次，可用率 %.1f%%，平均响应 %.0f ms，失败 %d 次\n",
				ts.Name, ts.URL, st, ts.Checks, ts.Uptime, ts.AvgMS, failN))
		}
	}
	if len(m.Targets) == 0 {
		sb.WriteString("（无监测目标）\n")
	}

	// 证书风险
	crows, err := r.Store.DB.Query(
		`SELECT name, last_cert_days FROM monitor_targets
		 WHERE user_id = ? AND target_type <> 'tcp' AND last_cert_days IS NOT NULL AND last_cert_days <= 7
		 ORDER BY last_cert_days`, userID)
	if err == nil {
		defer crows.Close()
		var certLines []string
		for crows.Next() {
			var name string
			var days int
			if crows.Scan(&name, &days) == nil {
				if days < 0 {
					m.CertRisks = append(m.CertRisks, fmt.Sprintf("%s：SSL 证书已过期", name))
					certLines = append(certLines, fmt.Sprintf("- %s：SSL 证书已过期", name))
				} else {
					m.CertRisks = append(m.CertRisks, fmt.Sprintf("%s：SSL 证书剩余 %d 天", name, days))
					certLines = append(certLines, fmt.Sprintf("- %s：SSL 证书剩余 %d 天", name, days))
				}
			}
		}
		if len(certLines) > 0 {
			sb.WriteString("\n## 证书风险\n")
			sb.WriteString(strings.Join(certLines, "\n") + "\n")
		}
	}

	// 2) 异常事件
	_ = r.Store.DB.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(status='open'),0) FROM anomalies WHERE user_id=? AND created_at BETWEEN ? AND ?`,
		userID, from, to).Scan(&m.AnomTotal, &m.AnomOpen)
	sb.WriteString(fmt.Sprintf("\n## 异常事件（窗口内共 %d 起，当前未处理 %d）\n", m.AnomTotal, m.AnomOpen))
	arows, err := r.Store.DB.Query(
		`SELECT created_at, type, severity, title, status FROM anomalies
		 WHERE user_id=? AND created_at BETWEEN ? AND ? ORDER BY id DESC LIMIT 30`, userID, from, to)
	if err == nil {
		defer arows.Close()
		n := 0
		for arows.Next() {
			var (
				t     time.Time
				typ   string
				sev   string
				title string
				st    string
			)
			if arows.Scan(&t, &typ, &sev, &title, &st) == nil {
				stLabel := "未处理"
				if st == "resolved" {
					stLabel = "已处理"
				}
				sb.WriteString(fmt.Sprintf("- [%s][%s] %s（%s）\n",
					t.Format("01-02 15:04"), sevLabelCN(sev), title, stLabel))
				n++
			}
		}
		if n == 0 {
			sb.WriteString("（无）\n")
		}
	}

	// 3) 错误日志
	lrows, err := r.Store.DB.Query(
		`SELECT level, COUNT(*) FROM logs WHERE user_id=? AND created_at BETWEEN ? AND ? GROUP BY level`,
		userID, from, to)
	if err == nil {
		defer lrows.Close()
		for lrows.Next() {
			var lv string
			var cnt int
			if lrows.Scan(&lv, &cnt) == nil {
				m.LogByLevel[lv] = cnt
			}
		}
	}
	srows, err := r.Store.DB.Query(
		`SELECT source, COUNT(*) AS cnt FROM logs WHERE user_id=? AND level IN ('error','fatal')
		 AND created_at BETWEEN ? AND ? GROUP BY source ORDER BY cnt DESC LIMIT 10`, userID, from, to)
	if err == nil {
		defer srows.Close()
		for srows.Next() {
			var src string
			var cnt int
			if srows.Scan(&src, &cnt) == nil {
				m.TopErrSource[src] = cnt
			}
		}
	}
	sb.WriteString("\n## 错误日志\n")
	if len(m.LogByLevel) == 0 {
		sb.WriteString("窗口内无任何日志\n")
	} else {
		parts := []string{}
		for _, lv := range []string{"fatal", "error", "warn", "info", "debug"} {
			if c, okk := m.LogByLevel[lv]; okk && c > 0 {
				parts = append(parts, fmt.Sprintf("%s %d 条", lv, c))
			}
		}
		sb.WriteString("级别分布: " + strings.Join(parts, "、") + "\n")
		if len(m.TopErrSource) > 0 {
			sp := []string{}
			for src, c := range m.TopErrSource {
				sp = append(sp, fmt.Sprintf("%s %d 条", src, c))
			}
			sb.WriteString("error/fatal TOP 来源: " + strings.Join(sp, "、") + "\n")
		}
	}
	// 最近错误样本
	erows, err := r.Store.DB.Query(
		`SELECT created_at, source, level, message FROM logs
		 WHERE user_id=? AND level IN ('error','fatal') AND created_at BETWEEN ? AND ?
		 ORDER BY id DESC LIMIT 20`, userID, from, to)
	if err == nil {
		defer erows.Close()
		var samples []string
		for erows.Next() {
			var (
				t   time.Time
				src string
				lv  string
				msg string
			)
			if erows.Scan(&t, &src, &lv, &msg) == nil {
				if len(msg) > 200 {
					msg = msg[:200] + "..."
				}
				samples = append(samples, fmt.Sprintf("- [%s][%s][%s] %s", t.Format("01-02 15:04"), src, lv, msg))
			}
		}
		if len(samples) > 0 {
			sb.WriteString("\n最近错误样本:\n")
			sb.WriteString(strings.Join(samples, "\n") + "\n")
		}
	}

	return m, sb.String(), nil
}

func sevLabelCN(s string) string {
	if s == "critical" {
		return "严重"
	}
	return "警告"
}

// fallbackReport AI 未启用时的规则引擎直出报告
func fallbackReport(kind string, m *metrics, from, to time.Time) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s（%s）\n\n", kindLabel(kind), from.Format("2006-01-02 15:04")+" ~ "+to.Format("2006-01-02 15:04")))
	b.WriteString("## 一、总体概览\n")
	up := 0
	down := 0
	for _, t := range m.Targets {
		if t.Status == "up" {
			up++
		} else if t.Status == "down" {
			down++
		}
	}
	totalChecks := 0
	for _, t := range m.Targets {
		totalChecks += t.Checks
	}
	logs := 0
	for _, c := range m.LogByLevel {
		logs += c
	}
	fmt.Fprintf(&b, "- 监测目标 **%d** 个：在线 %d，离线 %d\n", len(m.Targets), up, down)
	fmt.Fprintf(&b, "- 窗口内共探测 **%d** 次，异常事件 **%d** 起（未处理 %d）\n", totalChecks, m.AnomTotal, m.AnomOpen)
	fmt.Fprintf(&b, "- 日志 **%d** 条", logs)
	for _, lv := range []string{"fatal", "error", "warn"} {
		if c, okk := m.LogByLevel[lv]; okk && c > 0 {
			fmt.Fprintf(&b, "（其中 %s %d 条）", lv, c)
		}
	}
	b.WriteString("\n\n## 二、各目标表现\n")
	if len(m.Targets) == 0 {
		b.WriteString("（无监测目标）\n")
	} else {
		b.WriteString("| 目标 | 当前状态 | 探测次数 | 可用率 | 平均响应 |\n|---|---|---|---|---|\n")
		for _, t := range m.Targets {
			st := map[string]string{"up": "在线", "down": "离线", "unknown": "未检查"}[t.Status]
			fmt.Fprintf(&b, "| %s | %s | %d | %.1f%% | %.0f ms |\n", t.Name, st, t.Checks, t.Uptime, t.AvgMS)
		}
	}
	b.WriteString("\n## 三、异常事件\n")
	if m.AnomTotal == 0 {
		b.WriteString("窗口内无异常，一切正常。\n")
	} else {
		b.WriteString(fmt.Sprintf("共 %d 起，当前未处理 %d 起，详见「异常告警」页面。\n", m.AnomTotal, m.AnomOpen))
	}
	b.WriteString("\n## 四、错误日志\n")
	if len(m.TopErrSource) == 0 {
		b.WriteString("无 error/fatal 级日志。\n")
	} else {
		for src, c := range m.TopErrSource {
			fmt.Fprintf(&b, "- %s：%d 条\n", src, c)
		}
	}
	if len(m.CertRisks) > 0 {
		b.WriteString("\n## 五、证书风险\n")
		for _, c := range m.CertRisks {
			b.WriteString("- " + c + "\n")
		}
	}
	b.WriteString("\n> 本报告由规则引擎自动生成（AI 报告未启用或生成失败），在「设置 → AI 模型」启用 LLM 后可获得智能分析。\n")
	return b.String()
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

var _ = context.Background
