package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sitesentry/internal/auth"
	"sitesentry/internal/llm"
)

const aiSystemPrompt = "你是 SiteSentry 网站监测系统的 AI 运维助手（由大语言模型驱动）。" +
	"你擅长解释网站故障、分析日志与异常告警、给出可执行的排查与修复建议。" +
	"请用中文回答，使用 Markdown 格式，结论明确、条理清晰。"

// ListConversations 会话列表
func (h *Handler) ListConversations(c *gin.Context) {
	u := auth.UserFrom(c)
	rows, err := h.St.DB.Query(
		`SELECT cv.id, cv.title, cv.created_at,
			(SELECT COUNT(*) FROM llm_messages m WHERE m.conv_id = cv.id) AS msg_count,
			(SELECT MAX(m.created_at) FROM llm_messages m WHERE m.conv_id = cv.id) AS last_at
		 FROM llm_conversations cv WHERE cv.user_id = ? ORDER BY cv.id DESC LIMIT 50`, u.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type row struct {
		ID       uint64     `json:"id"`
		Title    string     `json:"title"`
		Created  time.Time  `json:"created_at"`
		MsgCount int        `json:"message_count"`
		LastAt   *time.Time `json:"last_at"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if rows.Scan(&r.ID, &r.Title, &r.Created, &r.MsgCount, &r.LastAt) == nil {
			out = append(out, r)
		}
	}
	ok(c, out)
}

// CreateConversation 新建会话
func (h *Handler) CreateConversation(c *gin.Context) {
	u := auth.UserFrom(c)
	title := "新对话"
	var body struct {
		Title string `json:"title"`
	}
	_ = parseJSON(c, &body)
	if body.Title != "" {
		title = strings.TrimSpace(body.Title)
		if len(title) > 100 {
			title = title[:100]
		}
	}
	res, err := h.St.DB.Exec(`INSERT INTO llm_conversations (user_id, title) VALUES (?, ?)`, u.ID, title)
	if err != nil {
		fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	id, _ := res.LastInsertId()
	ok(c, gin.H{"id": id})
}

// DeleteConversation 删除会话
func (h *Handler) DeleteConversation(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	_, _ = h.St.DB.Exec(`DELETE FROM llm_messages WHERE conv_id IN (SELECT id FROM llm_conversations WHERE id = ? AND user_id = ?)`, id, u.ID)
	_, _ = h.St.DB.Exec(`DELETE FROM llm_conversations WHERE id = ? AND user_id = ?`, id, u.ID)
	ok(c, gin.H{"deleted": id})
}

// ConversationMessages 会话消息
func (h *Handler) ConversationMessages(c *gin.Context) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	var owner uint64
	if err := h.St.DB.QueryRow(`SELECT user_id FROM llm_conversations WHERE id = ?`, id).Scan(&owner); err != nil || owner != u.ID {
		notFound(c, "会话不存在")
		return
	}
	rows, err := h.St.DB.Query(
		`SELECT role, content, created_at FROM llm_messages WHERE conv_id = ? ORDER BY id ASC LIMIT 200`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	type msg struct {
		Role      string    `json:"role"`
		Content   string    `json:"content"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []msg{}
	for rows.Next() {
		var m msg
		if rows.Scan(&m.Role, &m.Content, &m.CreatedAt) == nil {
			out = append(out, m)
		}
	}
	ok(c, out)
}

type sendMsgReq struct {
	Content     string `json:"content"`
	WithContext bool   `json:"with_context"`
}

// prepareAIRequest 校验会话归属、保存用户消息并组装 LLM 消息序列。
// ready=false 时响应已写出（错误），调用方直接 return。
func (h *Handler) prepareAIRequest(c *gin.Context) (convID uint64, messages []llm.Message, content string, ready bool) {
	u := auth.UserFrom(c)
	id, okk := idParam(c)
	if !okk {
		badReq(c, "无效 ID")
		return
	}
	var owner uint64
	if err := h.St.DB.QueryRow(`SELECT user_id FROM llm_conversations WHERE id = ?`, id).Scan(&owner); err != nil || owner != u.ID {
		notFound(c, "会话不存在")
		return
	}
	var req sendMsgReq
	if err := parseJSON(c, &req); err != nil {
		badReq(c, "请求格式错误")
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		badReq(c, "消息不能为空")
		return
	}
	if len(req.Content) > 8000 {
		badReq(c, "消息过长（最大 8000 字符）")
		return
	}

	// 组装消息序列（vLLM 要求 system 消息必须位于开头且唯一，故合并）
	sysContent := aiSystemPrompt
	if req.WithContext {
		if digest := h.Det.UserDigest(u.ID); digest != "" {
			sysContent += "\n\n以下是该用户当前的系统数据快照，回答时请结合这些信息：\n\n" + digest
		}
	}
	messages = []llm.Message{{Role: "system", Content: sysContent}}
	rows, err := h.St.DB.Query(
		`SELECT role, content FROM llm_messages WHERE conv_id = ? AND role IN ('user','assistant') ORDER BY id DESC LIMIT 16`, id)
	if err == nil {
		defer rows.Close()
		var hist []llm.Message
		for rows.Next() {
			var role, text string
			if rows.Scan(&role, &text) == nil {
				hist = append(hist, llm.Message{Role: role, Content: text})
			}
		}
		for i, j := 0, len(hist)-1; i < j; i, j = i+1, j-1 {
			hist[i], hist[j] = hist[j], hist[i]
		}
		messages = append(messages, hist...)
	}
	messages = append(messages, llm.Message{Role: "user", Content: req.Content})

	// 先落库用户消息（流式回复在结束后落库），首条消息时更新会话标题
	now := time.Now()
	if _, err := h.St.DB.Exec(
		`INSERT INTO llm_messages (conv_id, role, content, created_at) VALUES (?, 'user', ?, ?)`, id, req.Content, now); err != nil {
		fail(c, http.StatusInternalServerError, "保存消息失败")
		return
	}
	var cnt int
	if err := h.St.DB.QueryRow(`SELECT COUNT(*) FROM llm_messages WHERE conv_id = ? AND role = 'user'`, id).Scan(&cnt); err == nil && cnt == 1 {
		title := req.Content
		if len(title) > 30 {
			title = title[:30] + "…"
		}
		_, _ = h.St.DB.Exec(`UPDATE llm_conversations SET title = ? WHERE id = ?`, title, id)
	}
	return id, messages, req.Content, true
}

// SendMessage 发送消息并获取 AI 回复（非流式，保留兼容）
func (h *Handler) SendMessage(c *gin.Context) {
	id, messages, _, ready := h.prepareAIRequest(c)
	if !ready {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 150*time.Second)
	defer cancel()
	reply, err := h.LLM.Chat(ctx, messages, 2000)
	if err != nil {
		fail(c, http.StatusBadGateway, "AI 回复失败: "+err.Error())
		return
	}
	if _, err := h.St.DB.Exec(
		`INSERT INTO llm_messages (conv_id, role, content, created_at) VALUES (?, 'assistant', ?, ?)`, id, reply, time.Now()); err != nil {
		fail(c, http.StatusInternalServerError, "保存消息失败")
		return
	}
	ok(c, gin.H{"reply": reply})
}

// StreamMessage 流式发送：SSE 增量推送 AI 回复
// 协议：data: {"delta":"..."} 文本片段 / data: {"error":"..."} 出错 / data: [DONE] 结束
func (h *Handler) StreamMessage(c *gin.Context) {
	id, messages, _, ready := h.prepareAIRequest(c)
	if !ready {
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 180*time.Second)
	defer cancel()
	var full strings.Builder
	_, err := h.LLM.StreamChat(ctx, messages, 2000, func(d string) {
		full.WriteString(d)
		b, _ := json.Marshal(map[string]string{"delta": d})
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	})
	reply := strings.TrimSpace(full.String())
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": "AI 回复失败: " + err.Error()})
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
	}
	if reply != "" {
		if _, serr := h.St.DB.Exec(
			`INSERT INTO llm_messages (conv_id, role, content, created_at) VALUES (?, 'assistant', ?, ?)`,
			id, reply, time.Now()); serr != nil {
			log.Printf("[ai] 流式回复落库失败: %v", serr)
		}
	}
	_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}
