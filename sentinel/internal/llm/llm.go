package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sitesentry/internal/store"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	Store *store.Store
	http  *http.Client
}

func New(st *store.Store) *Client {
	return &Client{
		Store: st,
		http:  &http.Client{Timeout: 120 * time.Second},
	}
}

type LLMConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Enabled bool
}

func (c *Client) Config() LLMConfig {
	st := c.Store
	baseURL := strings.TrimRight(st.GetSetting("llm_base_url", ""), "/")
	apiKey := st.GetSetting("llm_api_key", "")
	return LLMConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   st.GetSetting("llm_model", ""),
		// 未配置端点或 Key 时视为未启用，避免空配置产生无效调用
		Enabled: st.GetSetting("llm_enabled", "1") == "1" && baseURL != "" && apiKey != "",
	}
}

type chatRequest struct {
	Model              string    `json:"model"`
	Messages           []Message `json:"messages"`
	Temperature        float64   `json:"temperature"`
	MaxTokens          int       `json:"max_tokens"`
	Stream             bool      `json:"stream"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Chat 调用 OpenAI 兼容接口
func (c *Client) Chat(ctx context.Context, messages []Message, maxTokens int) (string, error) {
	cfg := c.Config()
	if !cfg.Enabled {
		return "", fmt.Errorf("LLM 已在系统设置中关闭")
	}
	if cfg.APIKey == "" {
		return "", fmt.Errorf("未配置 LLM API Key")
	}
	body, _ := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   maxTokens,
		Stream:      false,
		// 关闭 Qwen3 系列推理模型的思考过程，避免 token 预算被 reasoning 耗尽
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用 LLM 失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("LLM 返回 %d: %s", resp.StatusCode, truncate(string(data), 300))
		}
		return "", fmt.Errorf("解析 LLM 响应失败: %w", err)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return "", fmt.Errorf("LLM 错误: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("LLM 未返回内容 (usage: %d tokens)", cr.Usage.TotalTokens)
	}
	content := strings.TrimSpace(cr.Choices[0].Message.Content)
	if content == "" {
		fin := ""
		if cr.Choices[0].FinishReason != nil {
			fin = *cr.Choices[0].FinishReason
		}
		return "", fmt.Errorf("LLM 返回空内容（finish_reason=%s，token 预算可能不足，请调大 max_tokens）", fin)
	}
	return content, nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// StreamChat 以流式方式调用 OpenAI 兼容接口，每收到一段文本即回调 onDelta，返回完整内容
func (c *Client) StreamChat(ctx context.Context, messages []Message, maxTokens int, onDelta func(string)) (string, error) {
	cfg := c.Config()
	if !cfg.Enabled {
		return "", fmt.Errorf("LLM 已在系统设置中关闭")
	}
	if cfg.APIKey == "" {
		return "", fmt.Errorf("未配置 LLM API Key")
	}
	body, _ := json.Marshal(chatRequest{
		Model:              cfg.Model,
		Messages:           messages,
		Temperature:        0.3,
		MaxTokens:          maxTokens,
		Stream:             true,
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用 LLM 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("LLM 返回 %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var full strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return full.String(), fmt.Errorf("LLM 流式错误: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			d := chunk.Choices[0].Delta.Content
			full.WriteString(d)
			if onDelta != nil {
				onDelta(d)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), err
	}
	content := strings.TrimSpace(full.String())
	if content == "" {
		return "", fmt.Errorf("LLM 未返回内容")
	}
	return content, nil
}

// Ping 测试连通性
func (c *Client) Ping() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return c.Chat(ctx, []Message{
		{Role: "user", Content: "请用不超过 20 个字确认服务可用。"},
	}, 100)
}

// Diagnose 生成异常诊断报告（Markdown）
func (c *Client) Diagnose(anomalyType, severity, title, detail, contextBlock string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	sys := `你是一位资深的网站运维与故障诊断专家，服务于一个网站状态监测与日志收集系统（SiteSentry）。` +
		`用户会提供一条告警异常的详细信息、最近的探测记录和错误日志。` +
		`请你分析并输出中文 Markdown 诊断报告，必须包含以下三部分：` +
		`## 1. 根因分析（按可能性从高到低列出最可能的原因，逐条说明判断依据）` +
		`## 2. 排查步骤（给出具体、可立即执行的命令或操作）` +
		`## 3. 修复建议（短期止损方案 + 长期改进建议）` +
		`报告最后必须另起一行给出自动决策（供系统自动处理，该行格式固定，不要包含其他文字）：` +
		`DECISION: auto_resolve 或 DECISION: watch 或 DECISION: manual。` +
		`判断标准：` +
		`- auto_resolve：最近探测记录显示目标已恢复正常、或明显是一次性网络抖动/瞬时波动，无需人工介入；` +
		`- watch：问题可能仍存在但影响有限，需要继续观察，暂不需人工立即处理；` +
		`- manual：目标当前仍不可用或问题严重，需要人工尽快排查。` +
		`要求：结论明确、不空泛；如信息不足以确定根因，请明确指出还需要补充哪些信息。`
	user := fmt.Sprintf("异常类型: %s\n严重级别: %s\n标题: %s\n详情: %s\n\n%s\n/no_think",
		anomalyType, severity, title, detail, contextBlock)
	return c.Chat(ctx, []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}, 4000)
}

// Report 基于监测数据汇总生成中文 Markdown 运营报告（日报/周报通用）
func (c *Client) Report(contextBlock string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	sys := `你是一位资深网站运维分析师，为网站状态监测系统（SiteSentry）撰写运营报告。` +
		`用户会提供统计窗口内的完整监测数据：各目标可用率与探测统计、异常事件、错误日志分布与样本、证书风险。` +
		`请输出中文 Markdown 报告，必须包含以下五个部分：` +
		`## 一、总体概览（结论先行，2~4 句话概括整体健康度）` +
		`## 二、各目标表现（用表格列出：名称 / 当前状态 / 可用率 / 平均响应，并点名表现最好与最需要关注的目标）` +
		`## 三、异常与事件分析（对窗口内每类异常给出可能原因与影响面；无异常则明确说明）` +
		`## 四、错误日志分析（结合高频来源与错误样本，判断是否存在持续性/重复性问题）` +
		`## 五、风险与建议（按优先级列出 3~5 条具体可执行的建议，含证书到期等时效性风险）` +
		`要求：只基于给定数据，严禁虚构数字；数据不足处如实说明；语言简洁专业，不堆砌套话。`
	user := contextBlock + "\n/no_think"
	return c.Chat(ctx, []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}, 4000)
}

// ParseDecision 从诊断报告中提取 AI 自动决策（auto_resolve/watch/manual），无法识别时返回空串
func ParseDecision(analysis string) string {
	lines := strings.Split(analysis, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(strings.TrimSpace(lines[i]))
		if !strings.HasPrefix(t, "DECISION:") && !strings.HasPrefix(t, "DECISION：") {
			continue
		}
		t = strings.TrimPrefix(t, "DECISION:")
		t = strings.TrimPrefix(t, "DECISION：")
		t = strings.TrimSpace(t)
		for _, cand := range []string{"auto_resolve", "watch", "manual"} {
			if strings.HasPrefix(t, cand) {
				return cand
			}
		}
	}
	return ""
}

// StripDecision 从诊断报告中移除 DECISION 行（展示给用户的文本不含机器标记）
func StripDecision(analysis string) string {
	lines := strings.Split(analysis, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "DECISION:") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
