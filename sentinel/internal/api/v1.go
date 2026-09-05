package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
)

type tokenUser struct {
	ID        uint64
	TokenID   uint64
	LastUsed  *time.Time
}

// TokenAuth 校验上报令牌（Authorization: Bearer <token> 或 X-Api-Token）
func (h *Handler) TokenAuth(c *gin.Context) {
	token := ""
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	} else if h := c.GetHeader("X-Api-Token"); h != "" {
		token = strings.TrimSpace(h)
	}
	if token == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "缺少令牌（Header: Authorization: Bearer <token>）"})
		return
	}
	var (
		tokenID uint64
		userID  uint64
		enabled int
	)
	err := h.St.DB.QueryRow(
		`SELECT t.id, t.user_id, u.enabled FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token = ?`,
		token).Scan(&tokenID, &userID, &enabled)
	if err != nil || enabled != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "令牌无效或所属账号已禁用"})
		return
	}
	// 节流更新 last_used（5 分钟内不重复写库）
	var lastUsed *time.Time
	if err := h.St.DB.QueryRow(`SELECT last_used_at FROM api_tokens WHERE id = ?`, tokenID).Scan(&lastUsed); err == nil {
		if lastUsed == nil || time.Since(*lastUsed) > 5*time.Minute {
			_, _ = h.St.DB.Exec(`UPDATE api_tokens SET last_used_at = NOW() WHERE id = ?`, tokenID)
		}
	}
	c.Set("user", &auth.User{ID: userID, Enabled: 1})
	c.Set("token_id", tokenID)
	c.Next()
}

type reportLogReq struct {
	Source  string          `json:"source"`
	Level   string          `json:"level"`
	Message string          `json:"message"`
	Context json.RawMessage `json:"context"`
}

// ReportLog 外部网站上报日志
func (h *Handler) ReportLog(c *gin.Context) {
	u := auth.UserFrom(c)
	var req reportLogReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "JSON 格式错误")
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		req.Source = "api"
	}
	if len(req.Source) > 128 {
		badReq(c, "source 最长 128 字符")
		return
	}
	req.Level = strings.ToLower(strings.TrimSpace(req.Level))
	if !validLevels[req.Level] {
		badReq(c, "level 需为 debug/info/warn/error/fatal 之一")
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		badReq(c, "message 不能为空")
		return
	}
	if len(req.Message) > 20000 {
		badReq(c, "message 过长（最大 20000 字符）")
		return
	}
	ctxJSON := ""
	if len(req.Context) > 0 {
		if !json.Valid(req.Context) {
			badReq(c, "context 需为 JSON 对象")
			return
		}
		if len(req.Context) > 8192 {
			badReq(c, "context 过大（最大 8KB）")
			return
		}
		ctxJSON = string(req.Context)
	}
	if _, err := h.St.DB.Exec(
		`INSERT INTO logs (user_id, source, level, message, ctx) VALUES (?, ?, ?, ?, ?)`,
		u.ID, req.Source, req.Level, req.Message, nullIfEmpty(ctxJSON)); err != nil {
		fail(c, http.StatusInternalServerError, "写入日志失败")
		return
	}
	ok(c, gin.H{"accepted": true})
}

type reportAnomalyReq struct {
	Source   string `json:"source"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// ReportAnomaly 外部网站主动上报异常（进入异常队列并触发 AI 诊断 + 邮件）
func (h *Handler) ReportAnomaly(c *gin.Context) {
	u := auth.UserFrom(c)
	var req reportAnomalyReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "JSON 格式错误")
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		req.Source = "api"
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || len(req.Title) > 200 {
		badReq(c, "title 必填且最长 200 字符")
		return
	}
	req.Severity = strings.ToLower(strings.TrimSpace(req.Severity))
	if req.Severity != "critical" && req.Severity != "warning" {
		req.Severity = "warning"
	}
	if len(req.Detail) > 10000 {
		req.Detail = req.Detail[:10000]
	}
	detail := req.Detail
	if detail == "" {
		detail = req.Title
	}
	if len(detail) > 500 {
		detail = detail[:500] + "..."
	}
	h.Det.CreateAnomaly(u.ID, "external", req.Severity, nil, req.Source,
		"外部上报："+req.Title, detail, "open")
	ok(c, gin.H{"accepted": true, "message": "异常已接收，稍后完成 AI 诊断并发送通知邮件"})
}

// PingToken 令牌连通性测试
func (h *Handler) PingToken(c *gin.Context) {
	u := auth.UserFrom(c)
	ok(c, gin.H{"ok": true, "user_id": u.ID, "time": time.Now().Format(time.RFC3339)})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
