package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
)

// ListTokens 令牌列表
func (h *Handler) ListTokens(c *gin.Context) {
	u := auth.UserFrom(c)
	rows, err := h.St.DB.Query(
		`SELECT id, name, token, last_used_at, created_at FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID        uint64     `json:"id"`
		Name      string     `json:"name"`
		Token     string     `json:"token"`
		LastUsed  *time.Time `json:"last_used_at"`
		CreatedAt time.Time  `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if rows.Scan(&r.ID, &r.Name, &r.Token, &r.LastUsed, &r.CreatedAt) == nil {
			out = append(out, r)
		}
	}
	ok(c, out)
}

// CreateToken 创建上报令牌
func (h *Handler) CreateToken(c *gin.Context) {
	u := auth.UserFrom(c)
	var req struct {
		Name string `json:"name"`
	}
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "默认令牌"
	}
	if len(req.Name) > 100 {
		badReq(c, "名称过长")
		return
	}
	token := auth.RandomToken(24) // 48 位 hex
	res, err := h.St.DB.Exec(`INSERT INTO api_tokens (user_id, name, token) VALUES (?, ?, ?)`,
		u.ID, req.Name, token)
	if err != nil {
		fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	id, _ := res.LastInsertId()
	ok(c, gin.H{"id": id, "name": req.Name, "token": token})
}

// RevokeToken 吊销令牌
func (h *Handler) RevokeToken(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	res, err := h.St.DB.Exec(`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "操作失败")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(c, "令牌不存在")
		return
	}
	ok(c, gin.H{"revoked": id})
}
