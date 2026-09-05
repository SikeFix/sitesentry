package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
)

var validLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true, "fatal": true,
}

// ListLogs 日志查询（级别/来源/关键字/时间范围/分页）
func (h *Handler) ListLogs(c *gin.Context) {
	u := auth.UserFrom(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "30"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 30
	}
	where := []string{"user_id = ?"}
	args := []any{u.ID}
	if lv := c.Query("level"); lv != "" {
		if validLevels[lv] {
			where = append(where, "level = ?")
			args = append(args, lv)
		}
	}
	if src := strings.TrimSpace(c.Query("source")); src != "" {
		where = append(where, "source = ?")
		args = append(args, src)
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		where = append(where, "message LIKE ?")
		args = append(args, "%"+q+"%")
	}
	if from := c.Query("from"); from != "" {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", from, time.Local); err == nil {
			where = append(where, "created_at >= ?")
			args = append(args, t)
		}
	}
	if to := c.Query("to"); to != "" {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", to, time.Local); err == nil {
			where = append(where, "created_at <= ?")
			args = append(args, t)
		}
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := h.St.DB.QueryRow(`SELECT COUNT(*) FROM logs WHERE `+whereSQL, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	qargs := append(append([]any{}, args...), (page-1)*size, size)
	rows, err := h.St.DB.Query(
		fmt.Sprintf(`SELECT id, source, level, message, ctx, created_at FROM logs WHERE %s ORDER BY id DESC LIMIT ? OFFSET ?`, whereSQL),
		qargs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID        uint64    `json:"id"`
		Source    string    `json:"source"`
		Level     string    `json:"level"`
		Message   string    `json:"message"`
		Ctx       string    `json:"context"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		var ctx []byte
		if rows.Scan(&r.ID, &r.Source, &r.Level, &r.Message, &ctx, &r.CreatedAt) == nil {
			r.Ctx = string(ctx)
			out = append(out, r)
		}
	}
	ok(c, gin.H{"items": out, "total": total, "page": page, "size": size})
}

// LogSources 日志来源统计
func (h *Handler) LogSources(c *gin.Context) {
	u := auth.UserFrom(c)
	rows, err := h.St.DB.Query(
		`SELECT source, COUNT(*) AS cnt, MAX(created_at) AS last_at
		 FROM logs WHERE user_id = ? GROUP BY source ORDER BY cnt DESC LIMIT 100`, u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		Source  string    `json:"source"`
		Count   int       `json:"count"`
		LastAt  time.Time `json:"last_at"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if rows.Scan(&r.Source, &r.Count, &r.LastAt) == nil {
			out = append(out, r)
		}
	}
	ok(c, out)
}
