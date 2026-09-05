package api

import (
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
)

// Dashboard 概览数据
func (h *Handler) Dashboard(c *gin.Context) {
	u := auth.UserFrom(c)
	db := h.St.DB

	var (
		totalT, upT, downT int
		checksToday, openAnom, errLogsToday int
	)
	_ = db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(status='up'),0), COALESCE(SUM(status='down'),0)
		FROM monitor_targets WHERE user_id = ?`, u.ID).Scan(&totalT, &upT, &downT)
	_ = db.QueryRow(`SELECT COUNT(*) FROM checks WHERE user_id = ? AND DATE(checked_at) = CURDATE()`, u.ID).Scan(&checksToday)
	_ = db.QueryRow(`SELECT COUNT(*) FROM anomalies WHERE user_id = ? AND status = 'open'`, u.ID).Scan(&openAnom)
	_ = db.QueryRow(`SELECT COUNT(*) FROM logs WHERE user_id = ? AND DATE(created_at) = CURDATE() AND level IN ('error','fatal')`, u.ID).Scan(&errLogsToday)

	// 最近 7 天整体可用率
	type dayPoint struct {
		Date   string   `json:"date"`
		Uptime *float64 `json:"uptime"`
		Total  int      `json:"checks"`
	}
	days := []dayPoint{}
	for i := 6; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		dp := dayPoint{Date: d}
		var tot, okc int
		var up float64
		_ = db.QueryRow(
			`SELECT COUNT(*), COALESCE(SUM(ok),0), COALESCE(ROUND(100.0*SUM(ok)/COUNT(*),1),0)
			 FROM checks WHERE user_id = ? AND DATE(checked_at) = ?`, u.ID, d).
			Scan(&tot, &okc, &up)
		dp.Total = tot
		if tot > 0 {
			v := up
			dp.Uptime = &v
		}
		days = append(days, dp)
	}

	// 最近异常
	type anom struct {
		ID        uint64    `json:"id"`
		Type      string    `json:"type"`
		Severity  string    `json:"severity"`
		Title     string    `json:"title"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
	}
	anoms := []anom{}
	rows, _ := db.Query(
		`SELECT id, type, severity, title, status, created_at FROM anomalies
		 WHERE user_id = ? ORDER BY id DESC LIMIT 10`, u.ID)
	if rows != nil {
		for rows.Next() {
			var a anom
			if rows.Scan(&a.ID, &a.Type, &a.Severity, &a.Title, &a.Status, &a.CreatedAt) == nil {
				anoms = append(anoms, a)
			}
		}
		rows.Close()
	}

	// 24h 日志级别分布
	type lv struct {
		Level string `json:"level"`
		Count int    `json:"count"`
	}
	levels := []lv{}
	lrows, _ := db.Query(
		`SELECT level, COUNT(*) FROM logs WHERE user_id = ? AND created_at > DATE_SUB(NOW(), INTERVAL 24 HOUR) GROUP BY level`, u.ID)
	if lrows != nil {
		for lrows.Next() {
			var l lv
			if lrows.Scan(&l.Level, &l.Count) == nil {
				levels = append(levels, l)
			}
		}
		lrows.Close()
	}

	ok(c, gin.H{
		"targets":      gin.H{"total": totalT, "up": upT, "down": downT},
		"checks_today": checksToday,
		"open_anoms":   openAnom,
		"err_logs_24h": errLogsToday,
		"uptime_7d":    days,
		"recent_anoms": anoms,
		"log_levels":   levels,
	})
}
