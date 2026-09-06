package mailer

import (
	"fmt"
	"strings"
	"time"
)

// MDToHTML 极简 Markdown → HTML（供邮件嵌入 LLM 诊断报告）
// 支持: 标题(#/##/###)、加粗、行内代码、代码块、无序/有序列表、段落
func MDToHTML(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var b strings.Builder
	inCode := false
	inUL, inOL := false, false
	closeLists := func() {
		if inUL {
			b.WriteString("</ul>")
			inUL = false
		}
		if inOL {
			b.WriteString("</ol>")
			inOL = false
		}
	}
	flushPara := func() {}
	for _, raw := range lines {
		line := raw
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			closeLists()
			if inCode {
				b.WriteString("</pre>")
				inCode = false
			} else {
				b.WriteString("<pre style=\"background:#f4f4f5;border:1px solid #e4e4e7;border-radius:6px;padding:10px;font-size:12px;overflow-x:auto;\">")
				inCode = true
			}
			continue
		}
		if inCode {
			b.WriteString(EscapeHTML(line))
			b.WriteString("\n")
			continue
		}
		if trimmed == "" {
			closeLists()
			continue
		}
		// 标题
		if strings.HasPrefix(trimmed, "### ") {
			closeLists()
			b.WriteString("<h4 style=\"margin:14px 0 6px;color:#111827;\">")
			b.WriteString(EscapeHTML(trimmed[4:]))
			b.WriteString("</h4>")
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			closeLists()
			b.WriteString("<h3 style=\"margin:16px 0 6px;color:#111827;border-left:4px solid #2563eb;padding-left:8px;\">")
			b.WriteString(EscapeHTML(trimmed[3:]))
			b.WriteString("</h3>")
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			closeLists()
			b.WriteString("<h3 style=\"margin:16px 0 8px;color:#111827;\">")
			b.WriteString(EscapeHTML(trimmed[2:]))
			b.WriteString("</h3>")
			continue
		}
		// 无序列表
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			if !inUL {
				closeLists()
				b.WriteString("<ul style=\"margin:6px 0 6px 20px;padding:0;\">")
				inUL = true
			}
			b.WriteString("<li style=\"margin:3px 0;\">")
			b.WriteString(inlineMD(trimmed[2:]))
			b.WriteString("</li>")
			continue
		}
		// 有序列表
		if isOrderedItem(trimmed) {
			if !inOL {
				closeLists()
				b.WriteString("<ol style=\"margin:6px 0 6px 20px;padding:0;\">")
				inOL = true
			}
			idx := strings.Index(trimmed, ".")
			b.WriteString("<li style=\"margin:3px 0;\">")
			b.WriteString(inlineMD(trimmed[idx+2:]))
			b.WriteString("</li>")
			continue
		}
		closeLists()
		b.WriteString("<p style=\"margin:6px 0;line-height:1.7;\">")
		b.WriteString(inlineMD(trimmed))
		b.WriteString("</p>")
	}
	if inCode {
		b.WriteString("</pre>")
	}
	closeLists()
	_ = flushPara
	return b.String()
}

func isOrderedItem(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '1' && s[i] <= '9' {
		i++
	}
	return i > 0 && i < len(s) && s[i] == '.' && i+1 < len(s) && s[i+1] == ' '
}

// inlineMD 行内: 加粗 / 行内代码
func inlineMD(s string) string {
	// 先保护行内代码
	parts := strings.Split(s, "`")
	for i, p := range parts {
		if i%2 == 1 {
			parts[i] = "\x00CODE\x00" + EscapeHTML(p) + "\x00CODE\x00"
		} else {
			parts[i] = escapeAndBold(p)
		}
	}
	out := strings.Join(parts, "")
	out = strings.ReplaceAll(out, "\x00CODE\x00", "")
	return out
}

func escapeAndBold(s string) string {
	// 处理 **bold**
	seg := strings.Split(s, "**")
	for i, p := range seg {
		if i%2 == 1 {
			seg[i] = "<strong>" + EscapeHTML(p) + "</strong>"
		} else {
			seg[i] = EscapeHTML(p)
		}
	}
	return strings.Join(seg, "")
}

type alertTheme struct {
	Color string // 主题色
	Light string // 浅色底
	Line  string // 浅色边框
	Badge string // 徽章文案
}

func alertThemeOf(aType, severity string) alertTheme {
	switch aType {
	case "check_recovery":
		return alertTheme{"#16a34a", "#f0fdf4", "#bbf7d0", "已恢复"}
	case "check_down", "external":
		return alertTheme{"#dc2626", "#fef2f2", "#fecaca", "严重"}
	default:
		if severity == "critical" {
			return alertTheme{"#dc2626", "#fef2f2", "#fecaca", "严重"}
		}
		return alertTheme{"#d97706", "#fffbeb", "#fde68a", "警告"}
	}
}

var alertTypeLabel = map[string]string{
	"check_down":     "网站离线",
	"check_recovery": "网站恢复",
	"latency_spike":  "响应变慢",
	"log_burst":      "日志错误爆发",
	"external":       "外部上报异常",
	"reported":       "外部上报异常",
}

func alertInfoRow(label, value string) string {
	return `<tr>
		<td style="padding:7px 0;font-size:13px;color:#6b7280;width:84px;vertical-align:top;">` + label + `</td>
		<td style="padding:7px 0;font-size:13px;color:#111827;word-break:break-all;vertical-align:top;">` + value + `</td>
	</tr>`
}

// AlertEmail 构建异常/恢复告警邮件（HTML + 纯文本双版本）
func AlertEmail(appName, baseURL, aType, severity, title, detail, llmAnalysis, targetURL string) (html, plain string) {
	th := alertThemeOf(aType, severity)
	typeLabel := alertTypeLabel[aType]
	if typeLabel == "" {
		typeLabel = aType
	}
	now := time.Now()
	nowFull := now.Format("2006-01-02 15:04:05")
	detailHTML := EscapeHTML(detail)
	detailHTML = strings.ReplaceAll(detailHTML, "\n", "<br>")

	// 目标地址行（有 URL 时可点击）
	targetRow := `<span style="color:#9ca3af;">-</span>`
	if targetURL != "" {
		targetRow = `<a href="` + EscapeHTML(targetURL) + `" style="color:#2563eb;text-decoration:none;">` + EscapeHTML(targetURL) + `</a>`
	}

	// AI 诊断块
	analysisBlock := ""
	if llmAnalysis != "" {
		analysisBlock = `<div style="background:#eff6ff;border:1px solid #bfdbfe;border-radius:8px;padding:14px 16px;margin-top:18px;">
			<table style="width:100%;"><tr>
				<td style="font-size:13px;font-weight:700;color:#1d4ed8;">
					<span style="display:inline-block;background:#2563eb;color:#fff;font-size:10px;font-weight:700;padding:1px 7px;border-radius:4px;margin-right:7px;vertical-align:1px;">AI</span>智能诊断
				</td>
			</tr></table>
			<div style="font-size:13px;color:#1e3a5f;line-height:1.75;margin-top:8px;">` + MDToHTML(llmAnalysis) + `</div>
		</div>`
	}

	// 打开目标按钮（可选）
	targetBtn := ""
	if targetURL != "" {
		targetBtn = `<a href="` + EscapeHTML(targetURL) + `" style="display:inline-block;background:#fff;color:#374151;border:1px solid #d1d5db;font-size:13px;font-weight:600;text-decoration:none;padding:9px 20px;border-radius:8px;margin-left:10px;">打开目标站点</a>`
	}

	fontStack := `-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif`

	html = fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"></head>
<body style="margin:0;padding:0;background:#f3f4f6;">
<div style="max-width:600px;margin:0 auto;padding:28px 16px;font-family:%s;">
	<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#ffffff;border:1px solid #e5e7eb;border-radius:12px;overflow:hidden;">
		<tr><td style="height:4px;background:%s;font-size:0;line-height:0;">&nbsp;</td></tr>
		<tr><td style="background:#0f172a;padding:16px 24px;">
			<table role="presentation" width="100%%" cellpadding="0" cellspacing="0"><tr>
				<td style="color:#ffffff;font-size:16px;font-weight:700;">%s</td>
				<td align="right" style="color:#94a3b8;font-size:12px;">异常通知</td>
			</tr></table>
		</td></tr>
		<tr><td style="padding:20px 24px 8px;">
			<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:%s;border:1px solid %s;border-radius:8px;"><tr><td style="padding:12px 16px;">
				<table role="presentation" width="100%%" cellpadding="0" cellspacing="0"><tr>
					<td>
						<span style="display:inline-block;width:9px;height:9px;border-radius:50%%;background:%s;margin-right:8px;vertical-align:1px;"></span>
						<span style="font-size:14px;font-weight:700;color:%s;">%s</span>
						<span style="display:inline-block;background:%s;color:#ffffff;font-size:11px;font-weight:700;padding:2px 9px;border-radius:99px;margin-left:8px;vertical-align:1px;">%s</span>
					</td>
					<td align="right" style="color:#6b7280;font-size:12px;white-space:nowrap;">%s</td>
				</tr></table>
			</td></tr></table>
			<div style="font-size:17px;font-weight:700;color:#111827;margin:16px 0 10px;">%s</div>
			<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#f9fafb;border:1px solid #f1f5f9;border-radius:8px;"><tr><td style="padding:6px 14px;">
				%s
				%s
			</td></tr></table>
			<div style="margin-top:14px;background:#f9fafb;border:1px solid #f1f5f9;border-radius:8px;padding:12px 14px;font-size:13px;color:#374151;line-height:1.9;">%s</div>
			%s
		</td></tr>
		<tr><td style="padding:18px 24px 20px;border-top:1px solid #f1f5f9;text-align:center;">
			<a href="%s" style="display:inline-block;background:%s;color:#ffffff;font-size:13px;font-weight:600;text-decoration:none;padding:9px 22px;border-radius:8px;">查看告警详情</a>%s
			<div style="color:#9ca3af;font-size:11px;margin-top:14px;line-height:1.8;">
				此邮件由 %s 异常监测系统自动发送 · %s<br>
				如站点已自行恢复，可忽略本通知
			</div>
		</td></tr>
	</table>
</div>
</body></html>`,
		fontStack,
		th.Color,
		EscapeHTML(appName),
		th.Light, th.Line,
		th.Color, th.Color, EscapeHTML(typeLabel),
		th.Color, th.Badge,
		nowFull,
		EscapeHTML(title),
		alertInfoRow("告警类型", EscapeHTML(typeLabel)+" · "+th.Badge),
		alertInfoRow("目标地址", targetRow),
		detailHTML,
		analysisBlock,
		baseURL+"/admin/#/anomalies", th.Color, targetBtn,
		EscapeHTML(appName), nowFull)

	// ---------- 纯文本版本 ----------
	var p strings.Builder
	p.WriteString("【" + appName + "】异常通知：" + typeLabel + "（" + th.Badge + "）\n\n")
	p.WriteString("标题: " + title + "\n")
	p.WriteString("时间: " + nowFull + "\n")
	if targetURL != "" {
		p.WriteString("目标: " + targetURL + "\n")
	}
	p.WriteString("\n详情:\n" + detail + "\n")
	if llmAnalysis != "" {
		p.WriteString("\nAI 诊断:\n" + llmAnalysis + "\n")
	}
	p.WriteString("\n查看详情: " + baseURL + "/admin/#/anomalies\n")
	p.WriteString("此邮件由 " + appName + " 异常监测系统自动发送。")
	plain = p.String()
	return
}

// ReportEmail 构建智能报告邮件（HTML + 纯文本双版本）
func ReportEmail(appName, baseURL, kindLabel, title, contentMD string) (html, plain string) {
	now := time.Now().Format("2006-01-02 15:04:05")
	fontStack := `-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif`
	kindLabelEsc := EscapeHTML(kindLabel)

	html = fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"></head>
<body style="margin:0;padding:0;background:#f3f4f6;">
<div style="max-width:680px;margin:0 auto;padding:28px 16px;font-family:%s;">
	<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#ffffff;border:1px solid #e5e7eb;border-radius:12px;overflow:hidden;">
		<tr><td style="height:4px;background:#2563eb;font-size:0;line-height:0;">&nbsp;</td></tr>
		<tr><td style="background:#0f172a;padding:16px 24px;">
			<table role="presentation" width="100%%" cellpadding="0" cellspacing="0"><tr>
				<td style="color:#ffffff;font-size:16px;font-weight:700;">%s</td>
				<td align="right" style="color:#94a3b8;font-size:12px;">%s</td>
			</tr></table>
		</td></tr>
		<tr><td style="padding:20px 24px 8px;">
			<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#eff6ff;border:1px solid #bfdbfe;border-radius:8px;"><tr><td style="padding:12px 16px;">
				<span style="font-size:13px;font-weight:700;color:#1d4ed8;">%s</span>
				<span style="color:#64748b;font-size:12px;margin-left:10px;">%s</span>
			</td></tr></table>
			<div style="font-size:17px;font-weight:700;color:#111827;margin:16px 0 10px;">%s</div>
			<div style="font-size:13px;color:#1f2937;line-height:1.8;">%s</div>
		</td></tr>
		<tr><td style="padding:18px 24px 20px;border-top:1px solid #f1f5f9;text-align:center;">
			<a href="%s" style="display:inline-block;background:#2563eb;color:#ffffff;font-size:13px;font-weight:600;text-decoration:none;padding:9px 22px;border-radius:8px;">在后台查看完整报告</a>
			<div style="color:#9ca3af;font-size:11px;margin-top:14px;line-height:1.8;">
				此邮件由 %s 自动发送 · %s
			</div>
		</td></tr>
	</table>
</div>
</body></html>`,
		fontStack,
		EscapeHTML(appName), kindLabelEsc,
		kindLabelEsc, now,
		EscapeHTML(title),
		MDToHTML(contentMD),
		baseURL+"/admin/#/reports",
		EscapeHTML(appName), now)

	plain = "【" + appName + "】" + kindLabel + "：" + title + "\n\n" + contentMD + "\n\n" +
		"在后台查看完整报告: " + baseURL + "/admin/#/reports\n" +
		"此邮件由 " + appName + " 自动发送。"
	return
}

// TestEmail 构建邮件通道测试邮件（HTML + 纯文本）
func TestEmail(appName string) (html, plain string) {
	now := time.Now().Format("2006-01-02 15:04:05")
	fontStack := `-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif`
	html = fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"></head>
<body style="margin:0;padding:0;background:#f3f4f6;">
<div style="max-width:600px;margin:0 auto;padding:28px 16px;font-family:%s;">
	<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#ffffff;border:1px solid #e5e7eb;border-radius:12px;overflow:hidden;">
		<tr><td style="height:4px;background:#16a34a;font-size:0;line-height:0;">&nbsp;</td></tr>
		<tr><td style="background:#0f172a;padding:16px 24px;">
			<table role="presentation" width="100%%" cellpadding="0" cellspacing="0"><tr>
				<td style="color:#ffffff;font-size:16px;font-weight:700;">%s</td>
				<td align="right" style="color:#94a3b8;font-size:12px;">通道测试</td>
			</tr></table>
		</td></tr>
		<tr><td style="padding:28px 24px;text-align:center;">
			<div style="display:inline-block;width:52px;height:52px;border-radius:50%%;background:#f0fdf4;border:1px solid #bbf7d0;font-size:24px;line-height:52px;color:#16a34a;font-weight:700;">✓</div>
			<div style="font-size:17px;font-weight:700;color:#111827;margin:16px 0 8px;">邮件通道测试成功</div>
			<div style="font-size:13px;color:#6b7280;line-height:1.8;">
				这是 %s 发送的测试邮件，说明 SMTP 配置正常。<br>异常告警将使用同样的样式发送到该邮箱。
			</div>
			<div style="color:#9ca3af;font-size:11px;margin-top:20px;">发送时间 %s</div>
		</td></tr>
	</table>
</div>
</body></html>`,
		fontStack, EscapeHTML(appName), EscapeHTML(appName), now)
	plain = fmt.Sprintf("【%s】邮件通道测试成功\n\n这是 %s 发送的测试邮件，说明 SMTP 配置正常。\n发送时间 %s", appName, appName, now)
	return
}
