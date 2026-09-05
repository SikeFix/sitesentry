package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"sitesentry/internal/store"
)

var (
	ErrUserNotFound  = errors.New("用户不存在")
	ErrBadPassword   = errors.New("密码错误")
	ErrUserDisabled  = errors.New("账号已被禁用")
	ErrLocked        = errors.New("失败次数过多，账号已临时锁定 15 分钟")
	ErrNameTaken     = errors.New("用户名已存在")
	ErrInvalidCreds  = errors.New("用户名或密码错误")
	ErrBadOldPassword = errors.New("原密码不正确")
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type User struct {
	ID       uint64
	Username string
	Email    string
	Role     string
	Enabled  int
}

func (u *User) IsAdmin() bool { return u.Role == "admin" }

type Auth struct {
	Store    *store.Store
	Cookie   string
	MaxAgeSec int
}

func New(st *store.Store) *Auth {
	return &Auth{Store: st, Cookie: "ss_token", MaxAgeSec: int(30 * 24 * 3600)}
}

func HashPassword(p string) string {
	h, _ := bcrypt.GenerateFromPassword([]byte(p), 10)
	return string(h)
}

func VerifyPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// RandomToken 生成 n 字节的 hex 随机令牌
func RandomToken(n int) string { return randomToken(n) }

// CreateSession 为指定用户创建会话并下发 Cookie
func (a *Auth) CreateSession(c *gin.Context, userID uint64, token string) error {
	_, err := a.Store.DB.Exec(
		`INSERT INTO sessions (token, user_id, ip, ua, expires_at) VALUES (?, ?, ?, ?, ?)`,
		token, userID, c.ClientIP(), UserAgent(c.Request.UserAgent()),
		time.Now().Add(time.Duration(a.MaxAgeSec)*time.Second))
	if err != nil {
		return err
	}
	a.setCookie(c, token)
	return nil
}

func ValidateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return errors.New("用户名需为 3-32 位字母、数字或下划线")
	}
	return nil
}

func ValidateEmail(email string) error {
	if email == "" {
		return nil
	}
	if !emailRe.MatchString(email) {
		return errors.New("邮箱格式不正确")
	}
	return nil
}

// CreateUser 创建用户；首个用户自动成为 admin
func (a *Auth) CreateUser(username, email, password, role string) (*User, error) {
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidateEmail(email); err != nil {
		return nil, err
	}
	var n int
	if err := a.Store.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrNameTaken
	}
	if role != "admin" {
		role = "user"
	}
	res, err := a.Store.DB.Exec(
		`INSERT INTO users (username, email, password_hash, role) VALUES (?, ?, ?, ?)`,
		username, email, HashPassword(password), role)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: uint64(id), Username: username, Email: email, Role: role, Enabled: 1}, nil
}

// Login 校验凭据并创建会话
func (a *Auth) Login(c *gin.Context, username, password, ip, ua string) (*User, error) {
	var (
		id        uint64
		un        string
		email     string
		role      string
		enabled   int
		pwhash    string
		failCount int
		failAt    sql.NullTime
	)
	err := a.Store.DB.QueryRow(
		`SELECT id, username, email, role, enabled, password_hash, failed_count, failed_at
		 FROM users WHERE username = ?`, username).
		Scan(&id, &un, &email, &role, &enabled, &pwhash, &failCount, &failAt)
	if err == sql.ErrNoRows {
		return nil, ErrInvalidCreds
	}
	if err != nil {
		return nil, err
	}
	if enabled != 1 {
		return nil, ErrUserDisabled
	}
	// 锁定窗口
	if failCount >= 5 && failAt.Valid && time.Since(failAt.Time) < 15*time.Minute {
		return nil, ErrLocked
	}
	if !VerifyPassword(pwhash, password) {
		nc := failCount + 1
		_, _ = a.Store.DB.Exec(`UPDATE users SET failed_count = ?, failed_at = ? WHERE id = ?`,
			nc, time.Now(), id)
		if nc < 5 {
			return nil, fmt.Errorf("%w（还可尝试 %d 次）", ErrBadPassword, 5-nc)
		}
		return nil, ErrBadPassword
	}
	// 登录成功
	now := time.Now()
	_, _ = a.Store.DB.Exec(
		`UPDATE users SET failed_count = 0, failed_at = NULL, last_login_at = ? WHERE id = ?`, now, id)
	token := randomToken(32)
	if _, err := a.Store.DB.Exec(
		`INSERT INTO sessions (token, user_id, ip, ua, expires_at) VALUES (?, ?, ?, ?, ?)`,
		token, id, ip, ua, now.Add(time.Duration(a.MaxAgeSec)*time.Second)); err != nil {
		return nil, err
	}
	a.setCookie(c, token)
	return &User{ID: id, Username: un, Email: email, Role: role, Enabled: 1}, nil
}

func (a *Auth) setCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     a.Cookie,
		Value:    token,
		Path:     "/",
		MaxAge:   a.MaxAgeSec,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) ClearCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: a.Cookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
}

func (a *Auth) Logout(c *gin.Context) {
	if t, err := c.Cookie(a.Cookie); err == nil && t != "" {
		_, _ = a.Store.DB.Exec(`DELETE FROM sessions WHERE token = ?`, t)
	}
	a.ClearCookie(c)
}

// UserFromToken 校验会话令牌，返回用户
func (a *Auth) UserFromToken(token string) (*User, error) {
	var (
		id      uint64
		un      string
		email   string
		role    string
		enabled int
	)
	err := a.Store.DB.QueryRow(
		`SELECT u.id, u.username, u.email, u.role, u.enabled
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token = ? AND s.expires_at > NOW()`, token).
		Scan(&id, &un, &email, &role, &enabled)
	if err == sql.ErrNoRows {
		return nil, errors.New("会话无效或已过期")
	}
	if err != nil {
		return nil, err
	}
	if enabled != 1 {
		return nil, ErrUserDisabled
	}
	return &User{ID: id, Username: un, Email: email, Role: role, Enabled: 1}, nil
}

// Middleware 会话鉴权中间件
func (a *Auth) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(a.Cookie)
		if err != nil || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			return
		}
		u, err := a.UserFromToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "会话已过期，请重新登录"})
			return
		}
		c.Set("user", u)
		c.Next()
	}
}

func UserFrom(c *gin.Context) *User {
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*User); ok {
			return u
		}
	}
	return nil
}

func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		u := UserFrom(c)
		if u == nil || !u.IsAdmin() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "需要管理员权限"})
			return
		}
		c.Next()
	}
}

// UserAgent 截断 UA
func UserAgent(ua string) string {
	if len(ua) > 200 {
		return ua[:200]
	}
	return ua
}

// SplitEmails 拆分并清洗邮箱列表
func SplitEmails(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		e := strings.TrimSpace(p)
		if e != "" && emailRe.MatchString(e) {
			out = append(out, e)
		}
	}
	return out
}
