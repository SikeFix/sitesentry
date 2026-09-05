package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 10 * time.Second}

// 支持的群机器人类型
var validTypes = map[string]bool{"feishu": true, "dingtalk": true, "wechat": true, "custom": true}

func ValidType(t string) bool { return validTypes[t] }

// Send 向群机器人 Webhook 推送告警文本，返回错误（best effort，调用方记录日志即可）
func Send(url, typ, title, body string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("webhook 地址为空")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("webhook 地址需以 http(s) 开头")
	}
	typ = strings.TrimSpace(typ)
	if !validTypes[typ] {
		typ = "custom"
	}
	payload, err := buildPayload(typ, title, body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 webhook 失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook 返回 %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	// 飞书机器人 200 但业务码非 0 视为失败
	if typ == "feishu" {
		var r struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(data, &r) == nil && r.Code != 0 {
			return fmt.Errorf("飞书机器人拒绝: code=%d %s", r.Code, r.Msg)
		}
	}
	return nil
}

func buildPayload(typ, title, body string) ([]byte, error) {
	switch typ {
	case "feishu":
		lines := splitLines(body)
		var content [][]map[string]string
		for _, ln := range lines {
			content = append(content, []map[string]string{{"tag": "text", "text": ln}})
		}
		return json.Marshal(map[string]any{
			"msg_type": "post",
			"content": map[string]any{
				"post": map[string]any{
					"zh_cn": map[string]any{"title": title, "content": content},
				},
			},
		})
	case "dingtalk":
		return json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": title + "\n" + body},
		})
	case "wechat":
		return json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": title + "\n" + body},
		})
	default: // custom：直接发结构化 JSON，便于自定义消费
		return json.Marshal(map[string]any{
			"title":  title,
			"detail": body,
			"time":   time.Now().Format("2006-01-02 15:04:05"),
		})
	}
}

func splitLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimRight(ln, " \t")
		if ln != "" {
			out = append(out, ln)
		}
	}
	if len(out) == 0 {
		out = []string{"（无详情）"}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
