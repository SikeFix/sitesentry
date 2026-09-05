package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
)

// ListUsers 用户列表（管理员）
func (h *Handler) ListUsers(c *gin.Context) {
	rows, err := h.St.DB.Query(
		`SELECT id, username, email, role, enabled, created_at, last_login_at,
			(SELECT COUNT(*) FROM monitor_targets t WHERE t.user_id = users.id) AS targets,
			(SELECT COUNT(*) FROM anomalies a WHERE a.user_id = users.id AND a.status = 'open') AS open_anoms
		 FROM users ORDER BY id ASC`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID          uint64     `json:"id"`
		Username    string     `json:"username"`
		Email       string     `json:"email"`
		Role        string     `json:"role"`
		Enabled     int        `json:"enabled"`
		CreatedAt   time.Time  `json:"created_at"`
		LastLoginAt *time.Time `json:"last_login_at"`
		Targets     int        `json:"target_count"`
		OpenAnoms   int        `json:"open_anomalies"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if rows.Scan(&r.ID, &r.Username, &r.Email, &r.Role, &r.Enabled, &r.CreatedAt,
			&r.LastLoginAt, &r.Targets, &r.OpenAnoms) == nil {
			out = append(out, r)
		}
	}
	ok(c, out)
}

type createUserReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// CreateUser 新建用户（管理员）
func (h *Handler) CreateUser(c *gin.Context) {
	var req createUserReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	if len(req.Password) < 6 {
		badReq(c, "密码至少 6 位")
		return
	}
	if req.Role != "admin" {
		req.Role = "user"
	}
	u, err := h.A.CreateUser(req.Username, req.Email, req.Password, req.Role)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrNameTaken):
			fail(c, http.StatusConflict, "用户名已存在")
		default:
			badReq(c, err.Error())
		}
		return
	}
	ok(c, gin.H{"id": u.ID, "username": u.Username, "role": u.Role})
}

type updateUserReq struct {
	Role    string `json:"role"`
	Enabled *int   `json:"enabled"`
	Email   string `json:"email"`
}

// UpdateUser 修改用户（管理员）
func (h *Handler) UpdateUser(c *gin.Context) {
	me := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	if id == me.ID {
		badReq(c, "不能修改自己的账号")
		return
	}
	var targetRole string
	enabled := -1
	var email string
	err := h.St.DB.QueryRow(`SELECT role, enabled, email FROM users WHERE id = ?`, id).
		Scan(&targetRole, &enabled, &email)
	if err != nil {
		notFound(c, "用户不存在")
		return
	}
	var req updateUserReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	fields, args := []string{}, []any{}
	if req.Role != "" {
		if req.Role != "admin" && req.Role != "user" {
			badReq(c, "role 需为 admin 或 user")
			return
		}
		fields = append(fields, "role = ?")
		args = append(args, req.Role)
	}
	if req.Enabled != nil {
		fields = append(fields, "enabled = ?")
		args = append(args, *req.Enabled)
	}
	if req.Email != "" {
		if err := auth.ValidateEmail(req.Email); err != nil {
			badReq(c, err.Error())
			return
		}
		fields = append(fields, "email = ?")
		args = append(args, req.Email)
	}
	if len(fields) == 0 {
		badReq(c, "没有需要修改的字段")
		return
	}
	args = append(args, id)
	if _, err := h.St.DB.Exec(`UPDATE users SET `+strings.Join(fields, ", ")+` WHERE id = ?`, args...); err != nil {
		fail(c, http.StatusInternalServerError, "修改失败: "+err.Error())
		return
	}
	// 禁用时清除其会话
	if req.Enabled != nil && *req.Enabled == 0 {
		_, _ = h.St.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
	}
	ok(c, gin.H{"updated": id})
}

// DeleteUser 删除用户及其数据（管理员）
func (h *Handler) DeleteUser(c *gin.Context) {
	me := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	if id == me.ID {
		badReq(c, "不能删除自己的账号")
		return
	}
	tx, err := h.St.DB.Begin()
	if err != nil {
		fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	defer tx.Rollback()
	queries := []string{
		`DELETE FROM sessions WHERE user_id = ?`,
		`DELETE FROM llm_messages WHERE conv_id IN (SELECT id FROM llm_conversations WHERE user_id = ?)`,
		`DELETE FROM llm_conversations WHERE user_id = ?`,
		`DELETE FROM checks WHERE user_id = ?`,
		`DELETE FROM anomalies WHERE user_id = ?`,
		`DELETE FROM logs WHERE user_id = ?`,
		`DELETE FROM api_tokens WHERE user_id = ?`,
		`DELETE FROM monitor_targets WHERE user_id = ?`,
		`DELETE FROM mail_queue WHERE user_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	}
	for _, q := range queries {
		if _, err := tx.Exec(q, id); err != nil {
			fail(c, http.StatusInternalServerError, "删除失败: "+err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	ok(c, gin.H{"deleted": id})
}
