package api

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
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
	// 注意占位符顺序：MySQL LIMIT 行数为第一个占位符、OFFSET 为第二个
	qargs := append(append([]any{}, args...), size, (page-1)*size)
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

// ---------- 日志智能聚类 ----------

var (
	insURLRe  = regexp.MustCompile(`https?://[^\s,，"'<>）\]]+`)
	insIPRe   = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`)
	insHexRe  = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	insPathRe = regexp.MustCompile(`(?:/[\w.-]+){2,}`)
	insNumRe  = regexp.MustCompile(`\d+`)
)

// normalizeLogMessage 把日志消息中的可变部分（URL/IP/长哈希/路径/数字）归一为占位符，
// 使「同一类错误」聚成一个特征（signature），便于发现重复出现的故障模式。
func normalizeLogMessage(m string) string {
	m = insURLRe.ReplaceAllString(m, "<URL>")
	m = insIPRe.ReplaceAllString(m, "<IP>")
	m = insHexRe.ReplaceAllString(m, "<HEX>")
	m = insPathRe.ReplaceAllString(m, "<PATH>")
	m = insNumRe.ReplaceAllString(m, "<N>")
	m = strings.Join(strings.Fields(m), " ")
	r := []rune(m)
	if len(r) > 160 {
		m = string(r[:160])
	}
	return m
}

type logCluster struct {
	Signature string    `json:"signature"`
	Sample    string    `json:"sample"`
	Count     int       `json:"count"`
	Levels    []string  `json:"levels"`
	Sources   []string  `json:"sources"`
	FirstAt   time.Time `json:"first_at"`
	LastAt    time.Time `json:"last_at"`
}

// LogInsights 近 7 天 warn/error/fatal 日志的智能聚类：
// 返回出现频次最高的错误模式（同一来源 + 同一归一化特征），用于快速定位反复出现的故障。
func (h *Handler) LogInsights(c *gin.Context) {
	u := auth.UserFrom(c)
	from := time.Now().AddDate(0, 0, -7)
	rows, err := h.St.DB.Query(
		`SELECT source, level, message, created_at FROM logs
		 WHERE user_id = ? AND level IN ('warn','error','fatal') AND created_at >= ?
		 ORDER BY id DESC LIMIT 5000`, u.ID, from)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()

	type agg struct {
		sample   string
		count    int
		levels   map[string]bool
		sources  map[string]bool
		firstAt  time.Time
		lastAt   time.Time
	}
	groups := map[string]*agg{}
	scanned := 0
	for rows.Next() {
		var (
			src, lv, msg string
			t            time.Time
		)
		if rows.Scan(&src, &lv, &msg, &t) != nil {
			continue
		}
		scanned++
		sig := normalizeLogMessage(msg)
		if sig == "" {
			continue
		}
		key := src + "\x00" + sig
		a, okk := groups[key]
		if !okk {
			s := msg
			if len([]rune(s)) > 300 {
				s = string([]rune(s)[:300]) + "…"
			}
			a = &agg{sample: s, firstAt: t, lastAt: t, levels: map[string]bool{}, sources: map[string]bool{}}
			groups[key] = a
		}
		a.count++
		a.levels[lv] = true
		a.sources[src] = true
		if t.Before(a.firstAt) {
			a.firstAt = t
		}
		if t.After(a.lastAt) {
			a.lastAt = t
		}
	}
	out := []logCluster{}
	for key, a := range groups {
		sig := key[strings.IndexByte(key, '\x00')+1:]
		srcs := []string{}
		for s := range a.sources {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		lvs := []string{}
		for _, cand := range []string{"fatal", "error", "warn"} {
			if a.levels[cand] {
				lvs = append(lvs, cand)
			}
		}
		out = append(out, logCluster{
			Signature: sig, Sample: a.sample, Count: a.count,
			Levels: lvs, Sources: srcs, FirstAt: a.firstAt, LastAt: a.lastAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].LastAt.After(out[j].LastAt)
	})
	if len(out) > 20 {
		out = out[:20]
	}
	ok(c, gin.H{"range_days": 7, "scanned": scanned, "groups": out})
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
