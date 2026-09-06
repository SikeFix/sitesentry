package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
)

// ListReports 报告列表（最近 50 条）
func (h *Handler) ListReports(c *gin.Context) {
	u := auth.UserFrom(c)
	rows, err := h.St.DB.Query(
		`SELECT id, kind, title, status, error, want_email, sent, created_at
		 FROM ai_reports WHERE user_id = ? ORDER BY id DESC LIMIT 50`, u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID        uint64    `json:"id"`
		Kind      string    `json:"kind"`
		Title     string    `json:"title"`
		Status    string    `json:"status"`
		Error     string    `json:"error"`
		WantEmail int       `json:"want_email"`
		Sent      int       `json:"sent"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if rows.Scan(&r.ID, &r.Kind, &r.Title, &r.Status, &r.Error, &r.WantEmail, &r.Sent, &r.CreatedAt) == nil {
			out = append(out, r)
		}
	}
	ok(c, out)
}

// GetReport 报告详情
func (h *Handler) GetReport(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	var (
		kind, title, content, status, errStr string
		wantEmail, sent                       int
		createdAt                             time.Time
	)
	err := h.St.DB.QueryRow(
		`SELECT kind, title, content, status, error, want_email, sent, created_at
		 FROM ai_reports WHERE id = ? AND user_id = ?`, id, u.ID).
		Scan(&kind, &title, &content, &status, &errStr, &wantEmail, &sent, &createdAt)
	if err != nil {
		notFound(c, "报告不存在")
		return
	}
	ok(c, gin.H{
		"id": id, "kind": kind, "title": title, "content": content,
		"status": status, "error": errStr, "want_email": wantEmail,
		"sent": sent, "created_at": createdAt,
	})
}

// CreateReport 手动触发报告生成（异步，返回任务 ID 供轮询）
func (h *Handler) CreateReport(c *gin.Context) {
	u := auth.UserFrom(c)
	var req struct {
		Kind      string `json:"kind"`
		SendEmail bool   `json:"send_email"`
	}
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	if req.Kind != "weekly" {
		req.Kind = "daily"
	}
	// 同类型生成中则直接复用，避免并发重复生成
	var busy uint64
	_ = h.St.DB.QueryRow(
		`SELECT id FROM ai_reports WHERE user_id=? AND kind=? AND status='pending' ORDER BY id DESC LIMIT 1`,
		u.ID, req.Kind).Scan(&busy)
	if busy > 0 {
		ok(c, gin.H{"id": busy, "reused": true})
		return
	}
	id, err := h.Rep.Create(u.ID, req.Kind, req.SendEmail)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"id": id, "reused": false})
}

// SendReportEmail 将已完成报告补发邮件
func (h *Handler) SendReportEmail(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	n, err := h.Rep.SendEmail(id, u.ID)
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	_, _ = h.St.DB.Exec(`UPDATE ai_reports SET sent = GREATEST(sent, 1) WHERE id = ? AND user_id = ?`, id, u.ID)
	ok(c, gin.H{"queued": n})
}

// DeleteReport 删除报告
func (h *Handler) DeleteReport(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	_, _ = h.St.DB.Exec(`DELETE FROM ai_reports WHERE id = ? AND user_id = ?`, id, u.ID)
	ok(c, gin.H{"deleted": id})
}
