package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
	"sitesentry/internal/mailer"
	"sitesentry/internal/webhook"
)

var emailRe2 = regexp.MustCompile(`^[^@\s,]+@[^@\s,]+\.[^@\s,]+$`)

// 允许通过 API 修改的设置项
var editableKeys = map[string]bool{
	"app_name": true, "base_url": true,
	"llm_base_url": true, "llm_api_key": true, "llm_model": true, "llm_enabled": true,
	"smtp_host": true, "smtp_port": true, "smtp_mode": true,
	"smtp_user": true, "smtp_pass": true, "smtp_from_name": true,
	"default_notify_emails": true,
	"log_burst_threshold": true, "latency_multiplier": true,
	"ai_auto_resolve": true,
	"webhook_type": true, "webhook_url": true,
}

// GetSettings 读取设置（管理员可见全部，普通用户可见部分）
func (h *Handler) GetSettings(c *gin.Context) {
	all := h.St.AllSettings()
	u := auth.UserFrom(c)
	if u.IsAdmin() {
		ok(c, all)
		return
	}
	safe := map[string]string{
		"app_name": all["app_name"], "base_url": all["base_url"],
		"llm_model": all["llm_model"], "llm_enabled": all["llm_enabled"],
		"default_notify_emails": all["default_notify_emails"],
		"log_burst_threshold": all["log_burst_threshold"],
		"latency_multiplier": all["latency_multiplier"],
		"ai_auto_resolve": all["ai_auto_resolve"],
	}
	ok(c, safe)
}

// SaveSettings 保存设置（仅管理员；空字符串表示不修改）
func (h *Handler) SaveSettings(c *gin.Context) {
	var body map[string]string
	if err := parseJSON(c, &body); err != nil {
		badReq(c, "请求格式错误（需为 JSON 对象）")
		return
	}
	changed := []string{}
	for k, v := range body {
		if !editableKeys[k] {
			continue
		}
		v = strings.TrimSpace(v)
		if k == "smtp_pass" && v == "" {
			continue // 留空表示保持原值
		}
		// 基础校验
		switch k {
		case "smtp_port":
			if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
				badReq(c, "smtp_port 需为 1-65535")
				return
			}
		case "smtp_mode":
			if v != "ssl" && v != "starttls" {
				badReq(c, "smtp_mode 需为 ssl 或 starttls")
				return
			}
		case "llm_enabled":
			if v != "0" && v != "1" {
				badReq(c, "llm_enabled 需为 0 或 1")
				return
			}
		case "ai_auto_resolve":
			if v != "0" && v != "1" {
				badReq(c, "ai_auto_resolve 需为 0 或 1")
				return
			}
		case "default_notify_emails":
			if v != "" {
				for _, e := range strings.Split(v, ",") {
					e = strings.TrimSpace(e)
					if e != "" && !emailRe2.MatchString(e) {
						badReq(c, "默认通知邮箱格式不正确: "+e)
						return
					}
				}
			}
		case "log_burst_threshold":
			if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 10000 {
				badReq(c, "log_burst_threshold 需为 1-10000")
				return
			}
		case "latency_multiplier":
			if v == "" {
				v = "3"
			}
		case "llm_base_url":
			if v != "" && !strings.HasPrefix(v, "http") {
				badReq(c, "llm_base_url 需以 http 开头")
				return
			}
		case "webhook_type":
			if v != "" && !webhook.ValidType(v) {
				badReq(c, "webhook_type 需为 feishu / dingtalk / wechat / custom")
				return
			}
		case "webhook_url":
			if v != "" && !strings.HasPrefix(v, "http") {
				badReq(c, "webhook_url 需以 http(s) 开头")
				return
			}
		}
		if err := h.St.SetSetting(k, v); err != nil {
			fail(c, http.StatusInternalServerError, "保存 "+k+" 失败: "+err.Error())
			return
		}
		changed = append(changed, k)
	}
	ok(c, gin.H{"changed": changed})
}

// TestMail 发送测试邮件
func (h *Handler) TestMail(c *gin.Context) {
	var req struct {
		To string `json:"to"`
	}
	_ = parseJSON(c, &req)
	to := strings.TrimSpace(req.To)
	if to == "" {
		// 取默认通知邮箱第一个
		for _, e := range strings.Split(h.St.GetSetting("default_notify_emails", ""), ",") {
			if e = strings.TrimSpace(e); e != "" {
				to = e
				break
			}
		}
	}
	if to == "" || !emailRe2.MatchString(to) {
		badReq(c, "请填写有效的收件邮箱")
		return
	}
	appName := h.St.GetSetting("app_name", "SiteSentry 哨兵")
	body, plain := mailer.TestEmail(appName)
	if err := h.Mail.EnqueueText(0, to, "[测试] SiteSentry 邮件通道测试", body, plain); err != nil {
		fail(c, http.StatusInternalServerError, "入队失败: "+err.Error())
		return
	}
	// 立即尝试发送
	sent := h.Mail.FlushQueue(5)
	if sent > 0 {
		ok(c, gin.H{"sent": true, "to": to})
		return
	}
	// 未立即发出（可能限速/认证失败），返回队列状态
	var lastErr string
	_ = h.St.DB.QueryRow(
		`SELECT last_error FROM mail_queue WHERE to_addr = ? AND status <> 'sent' ORDER BY id DESC LIMIT 1`, to).
		Scan(&lastErr)
	if lastErr != "" {
		fail(c, http.StatusBadGateway, "发送失败: "+lastErr)
		return
	}
	ok(c, gin.H{"queued": true, "to": to, "message": "已加入发送队列（可能被发信频率限制，将稍后自动发出）"})
}

// TestLLM 测试 LLM 连通性
func (h *Handler) TestLLM(c *gin.Context) {
	reply, err := h.LLM.Ping()
	if err != nil {
		fail(c, http.StatusBadGateway, "LLM 测试失败: "+err.Error())
		return
	}
	ok(c, gin.H{"reply": reply})
}

// TestWebhook 发送一条测试消息到群机器人
func (h *Handler) TestWebhook(c *gin.Context) {
	url := h.St.GetSetting("webhook_url", "")
	typ := h.St.GetSetting("webhook_type", "feishu")
	if strings.TrimSpace(url) == "" {
		badReq(c, "请先填写并保存 webhook 地址")
		return
	}
	body := "时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n类型: 通知测试\n详情: 这是一条来自 SiteSentry 的 Webhook 测试消息，收到即说明配置正常。"
	if err := webhook.Send(url, typ, "[测试] SiteSentry Webhook 通知测试", body); err != nil {
		fail(c, http.StatusBadGateway, "发送失败: "+err.Error())
		return
	}
	ok(c, gin.H{"sent": true})
}
