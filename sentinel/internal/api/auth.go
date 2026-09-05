package api

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
)

var emailListRe = regexp.MustCompile(`^[^@\s,]+@[^@\s,]+\.[^@\s,]+(,\s*[^@\s,]+@[^@\s,]+\.[^@\s,]+)*$`)

type registerReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register 注册（首个用户自动成为管理员）
func (h *Handler) Register(c *gin.Context) {
	var req registerReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	req.Username = trim(req.Username)
	req.Email = trim(req.Email)
	if len(req.Password) < 6 {
		badReq(c, "密码至少 6 位")
		return
	}
	role := "user"
	var n int
	if err := h.St.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err == nil && n == 0 {
		role = "admin"
	}
	u, err := h.A.CreateUser(req.Username, req.Email, req.Password, role)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrNameTaken):
			fail(c, http.StatusConflict, "用户名已存在")
		default:
			badReq(c, err.Error())
		}
		return
	}
	token := auth.RandomToken(32)
	if err := h.A.CreateSession(c, u.ID, token); err != nil {
		badReq(c, "创建会话失败")
		return
	}
	ok(c, gin.H{"user": publicUser(u), "token": token, "first_admin": u.IsAdmin()})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login 登录
func (h *Handler) Login(c *gin.Context) {
	var req loginReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	u, err := h.A.Login(c, trim(req.Username), req.Password, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCreds), errors.Is(err, auth.ErrBadPassword):
			fail(c, http.StatusUnauthorized, err.Error())
		case errors.Is(err, auth.ErrLocked):
			fail(c, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, auth.ErrUserDisabled):
			fail(c, http.StatusForbidden, err.Error())
		default:
			fail(c, http.StatusInternalServerError, "登录失败: "+err.Error())
		}
		return
	}
	ok(c, gin.H{"user": publicUser(u)})
}

// Logout 退出登录
func (h *Handler) Logout(c *gin.Context) {
	h.A.Logout(c)
	ok(c, gin.H{"logged_out": true})
}

// Me 当前用户
func (h *Handler) Me(c *gin.Context) {
	u := auth.UserFrom(c)
	if u == nil {
		fail(c, http.StatusUnauthorized, "未登录")
		return
	}
	ok(c, publicUser(u))
}

type changePwReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ChangePassword 修改当前用户密码
func (h *Handler) ChangePassword(c *gin.Context) {
	u := auth.UserFrom(c)
	var req changePwReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	if len(req.NewPassword) < 6 {
		badReq(c, "新密码至少 6 位")
		return
	}
	var hash string
	if err := h.St.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	if !auth.VerifyPassword(hash, req.OldPassword) {
		badReq(c, "原密码不正确")
		return
	}
	_, err := h.St.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`,
		auth.HashPassword(req.NewPassword), u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "修改失败")
		return
	}
	// 使其他会话失效
	_, _ = h.St.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, u.ID)
	h.A.ClearCookie(c)
	ok(c, gin.H{"changed": true, "need_login": true})
}

func publicUser(u *auth.User) gin.H {
	return gin.H{
		"id":       u.ID,
		"username": u.Username,
		"email":    u.Email,
		"role":     u.Role,
		"is_admin": u.IsAdmin(),
	}
}

func trim(s string) string {
	return regexp.MustCompile(`\s+`).ReplaceAllString(s, "")
}
