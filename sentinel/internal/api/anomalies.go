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

// ListAnomalies 异常列表
func (h *Handler) ListAnomalies(c *gin.Context) {
	u := auth.UserFrom(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	where := []string{"user_id = ?"}
	args := []any{u.ID}
	if st := c.Query("status"); st == "open" || st == "resolved" {
		where = append(where, "status = ?")
		args = append(args, st)
	}
	if t := c.Query("type"); t != "" {
		switch t {
		case "check_down", "check_recovery", "latency_spike", "log_burst", "external":
			where = append(where, "type = ?")
			args = append(args, t)
		}
	}
	if sv := c.Query("severity"); sv == "critical" || sv == "warning" {
		where = append(where, "severity = ?")
		args = append(args, sv)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := h.St.DB.QueryRow(`SELECT COUNT(*) FROM anomalies WHERE `+whereSQL, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	qargs := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := h.St.DB.Query(
		fmt.Sprintf(`SELECT id, type, severity, target_id, source, title, status, notified, ai_decision, resolved_at, llm_at, created_at,
			(SELECT t.name FROM monitor_targets t WHERE t.id = anomalies.target_id LIMIT 1) AS target_name
		 FROM anomalies WHERE %s ORDER BY id DESC LIMIT ? OFFSET ?`, whereSQL), qargs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID         uint64      `json:"id"`
		Type       string      `json:"type"`
		Severity   string      `json:"severity"`
		TargetID   *uint64     `json:"target_id"`
		TargetName *string     `json:"target_name"`
		Source     string      `json:"source"`
		Title      string      `json:"title"`
		Status     string      `json:"status"`
		Notified   int         `json:"notified"`
		AIDecision string      `json:"ai_decision"`
		ResolvedAt *time.Time  `json:"resolved_at"`
		LLMAt      *time.Time  `json:"llm_at"`
		CreatedAt  time.Time   `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		var tid int64
		if rows.Scan(&r.ID, &r.Type, &r.Severity, &tid, &r.Source, &r.Title,
			&r.Status, &r.Notified, &r.AIDecision, &r.ResolvedAt, &r.LLMAt, &r.CreatedAt, &r.TargetName) == nil {
			if tid > 0 {
				v := uint64(tid)
				r.TargetID = &v
			}
			out = append(out, r)
		}
	}
	ok(c, gin.H{"items": out, "total": total, "page": page, "size": size})
}

// GetAnomaly 异常详情
func (h *Handler) GetAnomaly(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	var (
		idv       uint64
		typ       string
		severity  string
		source    string
		title     string
		detail    string
		status    string
		notified  int
		createdAt time.Time
	)
	var targetID *uint64
	var llmText *string
	var targetName *string
	var aiDecision string
	var resolvedAt *time.Time
	err := h.St.DB.QueryRow(
		`SELECT id, type, severity, target_id, source, title, detail, status, notified, ai_decision, resolved_at, llm_analysis, created_at,
			(SELECT t.name FROM monitor_targets t WHERE t.id = anomalies.target_id LIMIT 1)
		 FROM anomalies WHERE id = ? AND user_id = ?`, id, u.ID).
		Scan(&idv, &typ, &severity, &targetID, &source, &title, &detail, &status, &notified, &aiDecision, &resolvedAt, &llmText, &createdAt, &targetName)
	if err != nil {
		notFound(c, "异常不存在")
		return
	}
	var tid int64
	if targetID != nil {
		tid = int64(*targetID)
	}
	// 关联目标的最近探测
	type chk struct {
		OK      int       `json:"ok"`
		Code    int       `json:"status_code"`
		MS      int       `json:"ms"`
		Err     string    `json:"error"`
		At      time.Time `json:"checked_at"`
	}
	var checks []chk
	if tid > 0 {
		rows, rerr := h.St.DB.Query(
			`SELECT ok, status_code, ms, error, checked_at FROM checks WHERE target_id = ? ORDER BY id DESC LIMIT 10`, tid)
		if rerr == nil {
			defer rows.Close()
			for rows.Next() {
				var k chk
				if rows.Scan(&k.OK, &k.Code, &k.MS, &k.Err, &k.At) == nil {
					checks = append(checks, k)
				}
			}
		}
	}
	ok(c, gin.H{
		"id": idv, "type": typ, "severity": severity, "target_id": tid,
		"target_name": targetName, "source": source, "title": title, "detail": detail,
		"status": status, "notified": notified, "ai_decision": aiDecision,
		"resolved_at": resolvedAt, "llm_analysis": llmText,
		"created_at": createdAt.Format("2006-01-02 15:04:05"), "recent_checks": checks,
	})
}

// ResolveAnomaly 标记已处理
func (h *Handler) ResolveAnomaly(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	res, err := h.St.DB.Exec(
		`UPDATE anomalies SET status = 'resolved', resolved_at = NOW() WHERE id = ? AND user_id = ? AND status = 'open'`,
		id, u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "操作失败")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(c, "异常不存在或已处理")
		return
	}
	ok(c, gin.H{"resolved": id})
}

// Reddiagnose 手动重新 AI 诊断
func (h *Handler) Reddiagnose(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	analysis, err := h.Det.Rediagnose(id, u.ID)
	if err != nil {
		fail(c, http.StatusBadGateway, "AI 诊断失败: "+err.Error())
		return
	}
	ok(c, gin.H{"llm_analysis": analysis})
}
