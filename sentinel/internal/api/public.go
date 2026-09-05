package api

import (
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// publicScanFloat 兼容 DECIMAL([]byte) 与 DOUBLE(float64) 两种驱动返回类型
func publicScanFloat(v interface{}) *float64 {
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

// PublicStatus 公开状态页数据（无需登录）
func (h *Handler) PublicStatus(c *gin.Context) {
	appName := h.St.GetSetting("app_name", "SiteSentry 哨兵")
	rows, err := h.St.DB.Query(`
		SELECT t.id, t.name, t.url, t.icon, t.status, t.last_ms, t.fail_streak,
			(t.maintenance_until IS NOT NULL AND t.maintenance_until > NOW()) AS in_maint,
			(SELECT ROUND(100.0*SUM(k.ok)/COUNT(*),1) FROM checks k WHERE k.target_id=t.id AND k.checked_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)) AS up24,
			(SELECT ROUND(100.0*SUM(k.ok)/COUNT(*),1) FROM checks k WHERE k.target_id=t.id AND k.checked_at > DATE_SUB(NOW(), INTERVAL 7 DAY))   AS up7,
			(SELECT ROUND(100.0*SUM(k.ok)/COUNT(*),1) FROM checks k WHERE k.target_id=t.id AND k.checked_at > DATE_SUB(NOW(), INTERVAL 30 DAY))  AS up30
		FROM monitor_targets t
		WHERE t.public = 1 AND t.enabled = 1
		ORDER BY t.id ASC`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type trow struct {
		ID              uint64
		Name            string
		URL             string
		Icon            string
		Status          string
		LastMS          sql.NullInt64
		FailStreak      int
		InMaint         bool
		Up24, Up7, Up30 interface{}
	}
	targets := []gin.H{}
	total, upN, downN, maintN := 0, 0, 0, 0
	for rows.Next() {
		var r trow
		if rows.Scan(&r.ID, &r.Name, &r.URL, &r.Icon, &r.Status, &r.LastMS, &r.FailStreak, &r.InMaint, &r.Up24, &r.Up7, &r.Up30) == nil {
			total++
			if r.InMaint {
				maintN++
			}
			if r.Status == "up" {
				upN++
			} else if r.Status == "down" {
				downN++
			}
			item := gin.H{
				"id":             r.ID,
				"name":           r.Name,
				"url":            r.URL,
				"icon":           r.Icon,
				"status":         r.Status,
				"in_maintenance": r.InMaint,
				"uptime_24h":     publicScanFloat(r.Up24),
				"uptime_7d":      publicScanFloat(r.Up7),
				"uptime_30d":     publicScanFloat(r.Up30),
			}
			if r.LastMS.Valid {
				item["last_ms"] = r.LastMS.Int64
			}
			targets = append(targets, item)
		}
	}

	// 最近事件（公开目标相关）
	incRows, err := h.St.DB.Query(`
		SELECT a.id, a.type, a.severity, a.title, a.created_at, a.resolved_at, a.status, t.name AS target_name
		FROM anomalies a JOIN monitor_targets t ON t.id = a.target_id
		WHERE t.public = 1 AND a.type IN ('check_down','check_recovery','latency_spike')
		ORDER BY a.id DESC LIMIT 20`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer incRows.Close()
	incidents := []gin.H{}
	openDown := false
	for incRows.Next() {
		var (
			id       uint64
			typ      string
			severity string
			title    string
			tname    string
			created  interface{}
			resolved interface{}
			status   string
		)
		if incRows.Scan(&id, &typ, &severity, &title, &created, &resolved, &status, &tname) == nil {
			incidents = append(incidents, gin.H{
				"id":          id,
				"type":        typ,
				"severity":    severity,
				"title":       title,
				"target_name": tname,
				"created_at":  created,
				"resolved_at": resolved,
				"status":      status,
			})
			if typ == "check_down" && status == "open" {
				openDown = true
			}
		}
	}

	overall := "operational"
	if downN > 0 || openDown {
		overall = "degraded"
	}
	ok(c, gin.H{
		"app_name": appName,
		"overall":  overall,
		"summary": gin.H{
			"targets_total":   total,
			"targets_up":      upN,
			"targets_down":    downN,
			"targets_maint":   maintN,
			"open_incidents":  countOpenIncidents(h),
		},
		"targets":   targets,
		"incidents": incidents,
	})
}

func countOpenIncidents(h *Handler) int {
	var n int
	_ = h.St.DB.QueryRow(`
		SELECT COUNT(*) FROM anomalies a
		JOIN monitor_targets t ON t.id = a.target_id
		WHERE t.public = 1 AND a.status = 'open' AND a.type IN ('check_down','latency_spike')`).Scan(&n)
	return n
}

// PublicTarget 单个公开目标的详情（30 天小时级状态 + 事件）
func (h *Handler) PublicTarget(c *gin.Context) {
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	var name, urlStr, icon, status string
	var pub, failStreak int
	var lastMS sql.NullInt64
	err := h.St.DB.QueryRow(
		`SELECT name, url, icon, public, status, fail_streak, last_ms FROM monitor_targets WHERE id = ?`, id).
		Scan(&name, &urlStr, &icon, &pub, &status, &failStreak, &lastMS)
	if err != nil || pub != 1 {
		notFound(c, "目标不存在或未公开")
		return
	}
	uptimeOf := func(days int) *float64 {
		var v interface{}
		if h.St.DB.QueryRow(
			`SELECT ROUND(100.0*SUM(ok)/COUNT(*),1) FROM checks
			 WHERE target_id=? AND checked_at > DATE_SUB(NOW(), INTERVAL ? DAY)`, id, days).Scan(&v) == nil {
			return publicScanFloat(v)
		}
		return nil
	}
	// 近 30 天按小时聚合：该小时任一次成功即视为正常
	rows, err := h.St.DB.Query(`
		SELECT DATE_FORMAT(checked_at, '%Y-%m-%d %H:00:00') AS h, MAX(ok) AS ok, ROUND(AVG(ms),0) AS ms
		FROM checks
		WHERE target_id = ? AND checked_at > DATE_SUB(NOW(), INTERVAL 30 DAY)
		GROUP BY h ORDER BY h ASC`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	defer rows.Close()
	hours := []gin.H{}
	for rows.Next() {
		var hStr string
		var okN int
		var ms interface{}
		if rows.Scan(&hStr, &okN, &ms) == nil {
			hours = append(hours, gin.H{"h": hStr, "ok": okN, "ms": ms})
		}
	}
	incRows, err := h.St.DB.Query(`
		SELECT id, type, severity, title, created_at, resolved_at, status
		FROM anomalies WHERE target_id = ? AND type IN ('check_down','check_recovery','latency_spike')
		ORDER BY id DESC LIMIT 10`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	defer incRows.Close()
	incidents := []gin.H{}
	for incRows.Next() {
		var (
			iid      uint64
			typ      string
			severity string
			title    string
			created  interface{}
			resolved interface{}
			st       string
		)
		if incRows.Scan(&iid, &typ, &severity, &title, &created, &resolved, &st) == nil {
			incidents = append(incidents, gin.H{
				"id": iid, "type": typ, "severity": severity, "title": title,
				"created_at": created, "resolved_at": resolved, "status": st,
			})
		}
	}
	item := gin.H{
		"id":          id,
		"name":        name,
		"url":         urlStr,
		"icon":        icon,
		"status":      status,
		"fail_streak": failStreak,
		"uptime_7d":   uptimeOf(7),
		"uptime_30d":  uptimeOf(30),
		"uptime_90d":  uptimeOf(90),
		"hours":       hours,
		"incidents":   incidents,
	}
	if lastMS.Valid {
		item["last_ms"] = lastMS.Int64
	}
	ok(c, item)
}

// PublicIcon 站点图标代理：服务端代为抓取目标站图标并返回，
// 规避目标站 Cross-Origin-Resource-Policy / 防盗链导致状态页 <img> 加载失败。
func (h *Handler) PublicIcon(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("url"))
	if raw == "" {
		badReq(c, "缺少 url 参数")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		badReq(c, "url 需为有效的 http/https 地址")
		return
	}
	// SSRF 防护：不代理内网 / 回环 / 链路本地地址
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		fail(c, http.StatusBadGateway, "图标域名无法解析")
		return
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			fail(c, http.StatusBadGateway, "图标地址不允许")
			return
		}
	}
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		fail(c, http.StatusBadGateway, "图标获取失败")
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; SiteSentry-IconFetcher/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		fail(c, http.StatusBadGateway, "图标获取失败")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail(c, http.StatusBadGateway, "图标源返回异常")
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(data) == 0 {
		fail(c, http.StatusBadGateway, "图标内容读取失败")
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		n := len(data)
		if n > 512 {
			n = 512
		}
		ct = http.DetectContentType(data[:n])
	}
	if !strings.HasPrefix(ct, "image/") {
		fail(c, http.StatusBadGateway, "不是图片资源")
		return
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, ct, data)
}

// GetBadge 可嵌入的状态徽章（shields.io 风格 SVG）：
// 在任意网页/README 中放置 <img src="https://your-domain/api/public/badge/1" alt="status"> 即可。
// 内容：在线（含 24h 可用率）/ 离线 / 维护中 / 无数据，60 秒缓存。
func (h *Handler) GetBadge(c *gin.Context) {
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	var name, status, targetType string
	var pub, inMaint int
	var up24 interface{}
	err := h.St.DB.QueryRow(`
		SELECT name, status, target_type, public,
		       (maintenance_until IS NOT NULL AND maintenance_until > NOW()),
		       (SELECT ROUND(100.0*SUM(ok)/COUNT(*),1) FROM checks k
		        WHERE k.target_id = ? AND k.checked_at > DATE_SUB(NOW(), INTERVAL 24 HOUR))
		FROM monitor_targets WHERE id = ?`, id, id).
		Scan(&name, &status, &targetType, &pub, &inMaint, &up24)
	if err != nil || pub != 1 {
		notFound(c, "目标不存在或未公开")
		return
	}

	label := name
	if runes := []rune(label); len(runes) > 12 {
		label = string(runes[:12]) + "…"
	}
	rightText, rightColor := "无数据", "#9f9f9f"
	switch {
	case inMaint == 1:
		rightText, rightColor = "维护中", "#9f9f9f"
	case status == "up":
		rightText = "在线"
		if f := publicScanFloat(up24); f != nil {
			rightText += fmt.Sprintf(" %.1f%%", *f)
		}
		rightColor = "#4caf50"
	case status == "down":
		rightText, rightColor = "离线", "#e05d5d"
	}

	// 宽度估算：ASCII 约 6.6px/字，CJK 约 13px/字（字号 11）
	widthOf := func(s string) int {
		w := 0
		for _, ch := range s {
			if ch > 0x2E7F {
				w += 13
			} else {
				w += 7
			}
		}
		return w + 16 // 两侧留白
	}
	leftW := widthOf(label)
	rightW := widthOf(rightText)
	totalW := leftW + rightW

	svg := fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img">`+
			`<title>%s: %s</title>`+
			`<rect width="%d" height="20" rx="3" fill="#55585e"/>`+
			`<path d="M %d 0 H %d A 3 3 0 0 1 %d 3 V 17 A 3 3 0 0 1 %d 20 H %d Z" fill="%s"/>`+
			`<text x="%d" y="14" text-anchor="middle" font-family="Verdana,DejaVu Sans,Helvetica,Arial,sans-serif" font-size="11" fill="#fff">%s</text>`+
			`<text x="%d" y="14" text-anchor="middle" font-family="Verdana,DejaVu Sans,Helvetica,Arial,sans-serif" font-size="11" fill="#fff">%s</text>`+
			`</svg>`,
		totalW, label, rightText,
		totalW,
		leftW, totalW-3, totalW, totalW, leftW, rightColor,
		leftW/2, label,
		leftW+rightW/2, rightText,
	)
	c.Header("Cache-Control", "public, max-age=60")
	c.Header("Content-Type", "image/svg+xml; charset=utf-8")
	c.String(http.StatusOK, svg)
}
