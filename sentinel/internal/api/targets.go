package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
	"sitesentry/internal/detector"
	"sitesentry/internal/monitor"
)

type targetReq struct {
	Name          string  `json:"name"`
	URL           string  `json:"url"`
	ExpectStatus  int     `json:"expect_status"`
	Keyword       string  `json:"keyword"`
	IntervalSec   int     `json:"interval_sec"`
	TimeoutSec    int     `json:"timeout_sec"`
	NotifyEmails  string  `json:"notify_emails"`
	NotifyRecover *int    `json:"notify_recovery"`
	Public        *int    `json:"public"`
	Icon          *string `json:"icon"`
	Enabled       *int    `json:"enabled"`
}

func validateTargetURL(s string) error {
	if s == "" {
		return errors.New("URL 不能为空")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("URL 需以 http:// 或 https:// 开头且格式正确")
	}
	return nil
}

func normalizeTargetReq(req *targetReq) error {
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	req.Keyword = strings.TrimSpace(req.Keyword)
	if req.Icon != nil {
		v := strings.TrimSpace(*req.Icon)
		req.Icon = &v
	}
	if req.Name == "" {
		return errors.New("名称不能为空")
	}
	if err := validateTargetURL(req.URL); err != nil {
		return err
	}
	if len(req.URL) > 500 {
		return errors.New("URL 过长")
	}
	if len(req.Keyword) > 200 {
		return errors.New("关键词过长")
	}
	if req.Icon != nil && len(*req.Icon) > 500 {
		return errors.New("图标地址过长")
	}
	if req.ExpectStatus < 0 || req.ExpectStatus > 599 {
		return errors.New("期望状态码需在 0-599 之间（0 表示不检查）")
	}
	if req.ExpectStatus == 0 {
		req.ExpectStatus = 200
	}
	if req.IntervalSec < 30 || req.IntervalSec > 3600 {
		return errors.New("探测间隔需在 30 秒 - 1 小时之间")
	}
	if req.TimeoutSec < 3 || req.TimeoutSec > 60 {
		return errors.New("超时需在 3-60 秒之间")
	}
	if req.NotifyEmails != "" {
		if !emailListRe.MatchString(strings.TrimSpace(req.NotifyEmails)) {
			return errors.New("通知邮箱格式不正确（多个邮箱用英文逗号分隔）")
		}
	}
	return nil
}

// ListTargets 目标列表（含 24h 统计）
func (h *Handler) ListTargets(c *gin.Context) {
	u := auth.UserFrom(c)
	rows, err := h.St.DB.Query(
		`SELECT t.id, t.name, t.url, t.expect_status, t.keyword, t.interval_sec, t.timeout_sec,
		        t.notify_emails, t.notify_recovery, t.public, t.icon, t.enabled, t.status, t.last_check_at,
		        t.last_status_code, t.last_ms, t.fail_streak, t.created_at,
		        (SELECT COUNT(*) FROM checks k WHERE k.target_id = t.id AND k.checked_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)) AS c24,
		        (SELECT ROUND(100.0*SUM(k.ok)/COUNT(*),1) FROM checks k WHERE k.target_id = t.id AND k.checked_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)) AS up24,
		        (SELECT AVG(k.ms) FROM checks k WHERE k.target_id = t.id AND k.ok = 1 AND k.checked_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)) AS avg24
		 FROM monitor_targets t WHERE t.user_id = ? ORDER BY t.id DESC`, u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID            uint64     `json:"id"`
		Name          string     `json:"name"`
		URL           string     `json:"url"`
		ExpectStatus  int        `json:"expect_status"`
		Keyword       string     `json:"keyword"`
		IntervalSec   int        `json:"interval_sec"`
		TimeoutSec    int        `json:"timeout_sec"`
		NotifyEmails  string     `json:"notify_emails"`
		NotifyRecover int        `json:"notify_recovery"`
		Public        int        `json:"public"`
		Icon          string     `json:"icon"`
		Enabled       int        `json:"enabled"`
		Status        string     `json:"status"`
		LastCheckAt   *time.Time `json:"last_check_at"`
		LastCode      *int       `json:"last_status_code"`
		LastMS        *int       `json:"last_ms"`
		FailStreak    int        `json:"fail_streak"`
		CreatedAt     time.Time  `json:"created_at"`
		Checks24      int        `json:"checks_24h"`
		Uptime24      *float64   `json:"uptime_24h"`
		AvgMS24       *float64   `json:"avg_ms_24h"`
	}
	// scanFloat 兼容 DECIMAL([]byte) 与 DOUBLE(float64) 两种驱动返回类型
	scanFloat := func(v interface{}) *float64 {
		switch x := v.(type) {
		case float64:
			return &x
		case []byte:
			if f, err := strconv.ParseFloat(string(x), 64); err == nil {
				return &f
			}
		case nil:
		}
		return nil
	}
	out := []row{}
	for rows.Next() {
		var r row
		var up24, avg24 interface{}
		if rows.Scan(&r.ID, &r.Name, &r.URL, &r.ExpectStatus, &r.Keyword, &r.IntervalSec,
			&r.TimeoutSec, &r.NotifyEmails, &r.NotifyRecover, &r.Public, &r.Icon, &r.Enabled, &r.Status,
			&r.LastCheckAt, &r.LastCode, &r.LastMS, &r.FailStreak, &r.CreatedAt,
			&r.Checks24, &up24, &avg24) == nil {
			if r.Checks24 > 0 {
				r.Uptime24 = scanFloat(up24)
				r.AvgMS24 = scanFloat(avg24)
			}
			out = append(out, r)
		}
	}
	ok(c, out)
}

func (h *Handler) getOwnedTarget(c *gin.Context) (uint64, bool) {
	u := auth.UserFrom(c)
	id, idOK := idParam(c)
	if !idOK {
		badReq(c, "无效 ID")
		return 0, false
	}
	var owner uint64
	if err := h.St.DB.QueryRow(`SELECT user_id FROM monitor_targets WHERE id = ?`, id).Scan(&owner); err != nil {
		notFound(c, "目标不存在")
		return 0, false
	}
	if owner != u.ID {
		c.AbortWithStatus(http.StatusForbidden)
		return 0, false
	}
	return id, true
}

// CreateTarget 创建监测目标
func (h *Handler) CreateTarget(c *gin.Context) {
	u := auth.UserFrom(c)
	var req targetReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	if err := normalizeTargetReq(&req); err != nil {
		badReq(c, err.Error())
		return
	}
	recover := 1
	if req.NotifyRecover != nil {
		recover = *req.NotifyRecover
	}
	pub := 1
	if req.Public != nil {
		pub = *req.Public
	}
	icon := ""
	if req.Icon != nil {
		icon = *req.Icon
	}
	res, err := h.St.DB.Exec(
		`INSERT INTO monitor_targets (user_id, name, url, expect_status, keyword, interval_sec, timeout_sec, notify_emails, notify_recovery, public, icon)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, req.Name, req.URL, req.ExpectStatus, req.Keyword, req.IntervalSec, req.TimeoutSec,
		req.NotifyEmails, recover, pub, icon)
	if err != nil {
		fail(c, http.StatusInternalServerError, "创建失败: "+err.Error())
		return
	}
	id, _ := res.LastInsertId()
	// 立即执行一次探测，让状态尽快有值
	go h.probeAndSave(uint64(id), false)
	ok(c, gin.H{"id": id})
}

// UpdateTarget 更新监测目标
func (h *Handler) UpdateTarget(c *gin.Context) {
	id, found := h.getOwnedTarget(c)
	if !found {
		return
	}
	var req targetReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	if err := normalizeTargetReq(&req); err != nil {
		badReq(c, err.Error())
		return
	}
	recover := 1
	if req.NotifyRecover != nil {
		recover = *req.NotifyRecover
	}
	enabled := 1
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	pub := 1
	if req.Public != nil {
		pub = *req.Public
	} else if err := h.St.DB.QueryRow(`SELECT public FROM monitor_targets WHERE id=?`, id).Scan(&pub); err != nil {
		pub = 1
	}
	// icon：未传则保留原值，传空串表示清除
	icon := ""
	if req.Icon != nil {
		icon = *req.Icon
	} else if err := h.St.DB.QueryRow(`SELECT COALESCE(icon,'') FROM monitor_targets WHERE id=?`, id).Scan(&icon); err != nil {
		icon = ""
	}
	_, err := h.St.DB.Exec(
		`UPDATE monitor_targets SET name=?, url=?, expect_status=?, keyword=?, interval_sec=?, timeout_sec=?,
		 notify_emails=?, notify_recovery=?, public=?, icon=?, enabled=? WHERE id=?`,
		req.Name, req.URL, req.ExpectStatus, req.Keyword, req.IntervalSec, req.TimeoutSec,
		req.NotifyEmails, recover, pub, icon, enabled, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "更新失败: "+err.Error())
		return
	}
	ok(c, gin.H{"id": id})
}

// DeleteTarget 删除目标（连带探测记录）
func (h *Handler) DeleteTarget(c *gin.Context) {
	id, found := h.getOwnedTarget(c)
	if !found {
		return
	}
	tx, err := h.St.DB.Begin()
	if err != nil {
		fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM checks WHERE target_id = ?`, id); err != nil {
		fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if _, err := tx.Exec(`UPDATE anomalies SET status='resolved', resolved_at=NOW() WHERE target_id = ? AND status='open'`, id); err != nil {
		fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if _, err := tx.Exec(`DELETE FROM monitor_targets WHERE id = ?`, id); err != nil {
		fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if err := tx.Commit(); err != nil {
		fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	ok(c, gin.H{"deleted": id})
}

// CheckNow 手动立即探测
func (h *Handler) CheckNow(c *gin.Context) {
	id, found := h.getOwnedTarget(c)
	if !found {
		return
	}
	r := h.probeAndSave(id, true)
	ok(c, r)
}

// probeAndSave 执行探测、落库并评估异常
func (h *Handler) probeAndSave(targetID uint64, notifyNow bool) gin.H {
	var (
		userID       uint64
		name, urlStr string
		expect, tmo  int
		keyword      string
		notifyRecov  int
	)
	err := h.St.DB.QueryRow(
		`SELECT user_id, name, url, expect_status, timeout_sec, keyword, notify_recovery
		 FROM monitor_targets WHERE id = ?`, targetID).
		Scan(&userID, &name, &urlStr, &expect, &tmo, &keyword, &notifyRecov)
	if err != nil {
		return gin.H{"ok": false, "error": "目标不存在"}
	}
	r := monitor.Probe(urlStr, tmo, expect, keyword)
	becameDown, becameUp := h.Det.RecordCheck(targetID, userID, r)
	h.Det.AfterCheck(detector.TargetInfo{
		ID: targetID, UserID: userID, Name: name, URL: urlStr, NotifyRecover: notifyRecov,
	}, becameDown, becameUp, r)
	if notifyNow {
		h.Det.ProcessPending(5)
	}
	st := "down"
	if r.OK {
		st = "up"
	}
	return gin.H{
		"ok":          r.OK,
		"status_code": r.StatusCode,
		"ms":          r.MS,
		"error":       r.Err,
		"note":        r.Note,
		"status":      st,
		"checked_at":  time.Now().Format("2006-01-02 15:04:05"),
	}
}

// TargetHistory 探测历史 + 统计
func (h *Handler) TargetHistory(c *gin.Context) {
	id, found := h.getOwnedTarget(c)
	if !found {
		return
	}
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours <= 0 || hours > 720 {
		hours = 24
	}
	rows, err := h.St.DB.Query(
		`SELECT id, ok, status_code, ms, error, checked_at FROM checks
		 WHERE target_id = ? AND checked_at > DATE_SUB(NOW(), INTERVAL ? HOUR)
		 ORDER BY id ASC LIMIT 500`, id, hours)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type pt struct {
		ID        uint64    `json:"id"`
		OK        int       `json:"ok"`
		Code      int       `json:"status_code"`
		MS        int       `json:"ms"`
		Err       string    `json:"error"`
		CheckedAt time.Time `json:"checked_at"`
	}
	pts := []pt{}
	for rows.Next() {
		var p pt
		if rows.Scan(&p.ID, &p.OK, &p.Code, &p.MS, &p.Err, &p.CheckedAt) == nil {
			pts = append(pts, p)
		}
	}
	var total, okc int
	var avg, mx int
	_ = h.St.DB.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(ok),0), COALESCE(ROUND(AVG(ms),0),0), COALESCE(MAX(ms),0)
		 FROM checks WHERE target_id = ? AND checked_at > DATE_SUB(NOW(), INTERVAL ? HOUR)`, id, hours).
		Scan(&total, &okc, &avg, &mx)
	var uptime float64
	if total > 0 {
		uptime = 100.0 * float64(okc) / float64(total)
	}
	ok(c, gin.H{
		"points": pts,
		"stats": gin.H{
			"total":  total,
			"ok":     okc,
			"uptime": uptime,
			"avg_ms": avg,
			"max_ms": mx,
		},
	})
}
