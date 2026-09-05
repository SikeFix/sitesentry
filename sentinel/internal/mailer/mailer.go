package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"sitesentry/internal/store"
)

const (
	FreqLimit  = 200 // 200 封 / 100 秒
	FreqWindow = 100 // 秒
	DailyLimit = 400 // 单发件人单日 400 封
)

type Mailer struct {
	Store *store.Store
}

func New(st *store.Store) *Mailer { return &Mailer{Store: st} }

type SMTPConfig struct {
	Host     string
	Port     int
	Mode     string // ssl | starttls
	User     string
	Pass     string
	FromName string
}

func (m *Mailer) SMTP() SMTPConfig {
	st := m.Store
	port := st.GetIntSetting("smtp_port", 465)
	return SMTPConfig{
		Host:     st.GetSetting("smtp_host", "smtp.feishu.cn"),
		Port:     port,
		Mode:     st.GetSetting("smtp_mode", "ssl"),
		User:     st.GetSetting("smtp_user", ""),
		Pass:     st.GetSetting("smtp_pass", ""),
		FromName: st.GetSetting("smtp_from_name", m.Store.GetSetting("app_name", "SiteSentry")),
	}
}

// SendOnce 立即发送一封邮件（含限速检查），plain 为可选纯文本版本
func (m *Mailer) SendOnce(to, subject, html, plain string) error {
	cfg := m.SMTP()
	if cfg.User == "" || cfg.Pass == "" {
		return fmt.Errorf("SMTP 未配置发件人账号或授权码")
	}
	// 频率限制：100 秒内已发 >= 200 封
	var recent int
	if err := m.Store.DB.QueryRow(
		`SELECT COUNT(*) FROM mail_log WHERE ok = 1 AND sent_at > DATE_SUB(NOW(), INTERVAL ? SECOND)`,
		FreqWindow).Scan(&recent); err != nil {
		return err
	}
	if recent >= FreqLimit {
		return fmt.Errorf("触发发信频率限制（%d 封/%d 秒），已延后发送", FreqLimit, FreqWindow)
	}
	// 单日限制
	var today int
	if err := m.Store.DB.QueryRow(
		`SELECT COUNT(*) FROM mail_log WHERE ok = 1 AND DATE(sent_at) = CURDATE()`).Scan(&today); err != nil {
		return err
	}
	if today >= DailyLimit {
		return fmt.Errorf("触发单日发信上限（%d 封），已延后发送", DailyLimit)
	}
	if err := m.deliver(cfg, to, subject, html, plain); err != nil {
		m.logMail(to, subject, false, err.Error())
		return err
	}
	m.logMail(to, subject, true, "")
	return nil
}

func (m *Mailer) deliver(cfg SMTPConfig, to, subject, html, plain string) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var client *smtp.Client
	var err error
	if cfg.Mode == "ssl" {
		// 465 隐式 TLS
		conn, derr := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if derr != nil {
			return fmt.Errorf("TLS 连接失败: %w", derr)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
	} else {
		// 587 STARTTLS
		conn, derr := net.DialTimeout("tcp", addr, 15*time.Second)
		if derr != nil {
			return fmt.Errorf("连接 SMTP 服务器失败: %w", derr)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err == nil {
			tlsCfg := &tls.Config{ServerName: cfg.Host}
			if serr := client.StartTLS(tlsCfg); serr != nil {
				client.Close()
				return fmt.Errorf("STARTTLS 失败: %w", serr)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("SMTP 客户端初始化失败: %w", err)
	}
	defer client.Close()
	if err := client.Hello("sitesentry.local"); err != nil {
		return fmt.Errorf("EHLO 失败: %w", err)
	}
	if err := authenticate(client, cfg.Host, cfg.User, cfg.Pass); err != nil {
		return fmt.Errorf("SMTP 认证失败（请检查发件人地址与授权码）: %w", err)
	}
	if err := client.Mail(cfg.User); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO 失败（收件地址被拒）: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败: %w", err)
	}
	if _, err := w.Write([]byte(buildMessage(cfg, to, subject, html, plain))); err != nil {
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("邮件发送被服务器拒绝: %w", err)
	}
	return client.Quit()
}

// authenticate 优先 PLAIN，失败则回退 LOGIN
func authenticate(client *smtp.Client, host, user, pass string) error {
	if err := client.Auth(smtp.PlainAuth("", user, pass, host)); err == nil {
		return nil
	}
	return client.Auth(&loginAuth{user: user, pass: pass})
}

// loginAuth 实现 SMTP AUTH LOGIN（RFC 4954）
type loginAuth struct {
	user string
	pass string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server == nil {
		return "", nil, fmt.Errorf("缺少服务器认证机制信息")
	}
	for _, m := range server.Auth {
		if strings.EqualFold(m, "LOGIN") {
			return "LOGIN", nil, nil
		}
	}
	return "", nil, fmt.Errorf("服务器未提供 PLAIN/LOGIN 认证机制: %v", server.Auth)
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	ch := strings.Trim(string(fromServer), "\r\n ")
	switch ch {
	case "VXNlcm5hbWU6", "Username:": // base64("Username:")
		return []byte(base64.StdEncoding.EncodeToString([]byte(a.user))), nil
	case "UGFzc3dvcmQ6", "Password:": // base64("Password:")
		return []byte(base64.StdEncoding.EncodeToString([]byte(a.pass))), nil
	}
	return nil, fmt.Errorf("意外的认证挑战: %q", ch)
}

func (m *Mailer) logMail(to, subject string, ok bool, errMsg string) {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	_, _ = m.Store.DB.Exec(
		`INSERT INTO mail_log (to_addr, subject, ok, error) VALUES (?, ?, ?, ?)`,
		to, truncate(subject, 250), boolInt(ok), errMsg)
}

// Enqueue 将邮件放入待发队列（仅 HTML）
func (m *Mailer) Enqueue(userID uint64, to, subject, html string) error {
	return m.EnqueueText(userID, to, subject, html, "")
}

// EnqueueText 将邮件放入待发队列（HTML + 可选纯文本）
func (m *Mailer) EnqueueText(userID uint64, to, subject, html, plain string) error {
	_, err := m.Store.DB.Exec(
		`INSERT INTO mail_queue (user_id, to_addr, subject, body_html, body_text, next_at) VALUES (?, ?, ?, ?, ?, NOW())`,
		userID, to, truncate(subject, 250), html, plain)
	return err
}

// FlushQueue 处理队列中到期的邮件（每个 tick 调用）
func (m *Mailer) FlushQueue(limit int) int {
	rows, err := m.Store.DB.Query(
		`SELECT id, to_addr, subject, body_html, COALESCE(body_text,'') FROM mail_queue
		 WHERE status = 'pending' AND next_at <= NOW() ORDER BY next_at ASC LIMIT ?`, limit)
	if err != nil {
		return 0
	}
	type item struct {
		id      uint64
		to      string
		subject string
		body    string
		plain   string
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.to, &it.subject, &it.body, &it.plain) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	sent := 0
	for _, it := range items {
		err := m.SendOnce(it.to, it.subject, it.body, it.plain)
		if err == nil {
			_, _ = m.Store.DB.Exec(`UPDATE mail_queue SET status = 'sent' WHERE id = ?`, it.id)
			sent++
		} else {
			res, _ := m.Store.DB.Exec(
				`UPDATE mail_queue SET attempts = attempts + 1, last_error = ?,
				 next_at = IF(attempts + 1 >= 10, next_at, DATE_ADD(NOW(), INTERVAL 5 MINUTE)),
				 status = IF(attempts + 1 >= 10, 'failed', 'pending') WHERE id = ?`,
				truncate(err.Error(), 500), it.id)
			if res != nil {
				if n, _ := res.RowsAffected(); n == 0 {
					// 若 attempts 达到 10 置为 failed
				}
			}
		}
	}
	return sent
}

// buildMessage 组装完整邮件报文。plain 非空时输出 multipart/alternative
//（text/plain + text/html，部分客户端默认显示纯文本或需手动切换）
func buildMessage(cfg SMTPConfig, to, subject, html, plain string) string {
	var b strings.Builder
	b.WriteString("From: " + encodeHeader(cfg.FromName) + " <" + cfg.User + ">\r\n")
	b.WriteString("To: <" + to + ">\r\n")
	b.WriteString("Subject: " + encodeText(subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n")
	b.WriteString("Message-ID: <" + fmt.Sprintf("%d.%s@sitesentry", time.Now().UnixNano(), randStr(6)) + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	if plain == "" {
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		b.WriteString(html)
		return b.String()
	}
	boundary := "alt" + randStr(16)
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(wrapBase64(plain))
	b.WriteString("\r\n--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(wrapBase64(html))
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.String()
}

// wrapBase64 base64 编码并按 700 字节折行（SMTP DATA 单行上限 998）
func wrapBase64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	const width = 700
	if len(enc) <= width {
		return enc
	}
	var b strings.Builder
	for i := 0; i < len(enc); i += width {
		end := i + width
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		if end < len(enc) {
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
