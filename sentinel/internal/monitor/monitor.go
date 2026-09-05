package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Result struct {
	OK         bool
	StatusCode int
	MS         int
	Err        string
	Note       string // 附加信息（如证书到期提醒）
	BodyLen    int
	CertDays   *int // 最近一次 TLS 探测的证书剩余天数（负数=已过期；非 HTTPS 为 nil）
}

// Probe 对目标 URL 执行一次 HTTP 探测
func Probe(url string, timeoutSec, expectStatus int, keyword string) Result {
	start := time.Now()
	r := Result{StatusCode: 0}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.MS = int(time.Since(start).Milliseconds())
		r.Err = "构造请求失败: " + err.Error()
		return r
	}
	req.Header.Set("User-Agent", "SiteSentry/1.0 (+https://github.com/sitesentry) Monitor Probe")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	client := &http.Client{
		Timeout: time.Duration(timeoutSec)*time.Second + 5*time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多(>5)")
			}
			return nil
		},
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS10},
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		r.MS = int(time.Since(start).Milliseconds())
		msg := err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = fmt.Sprintf("请求超时（超过 %d 秒未响应）", timeoutSec)
		}
		r.Err = prettifyNetErr(msg)
		return r
	}
	defer resp.Body.Close()

	r.MS = int(time.Since(start).Milliseconds())
	r.StatusCode = resp.StatusCode
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	r.BodyLen = len(body)

	// SSL 证书剩余天数（始终记录；临近到期时在 Note 中提示）
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		days := int(time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24)
		r.CertDays = &days
		if days <= 7 {
			if days < 0 {
				r.Note = "SSL 证书已过期"
			} else {
				r.Note = fmt.Sprintf("SSL 证书将于 %d 天后到期", days)
			}
		}
	}

	// 状态码断言
	if expectStatus > 0 && resp.StatusCode != expectStatus {
		r.Err = fmt.Sprintf("HTTP 状态码 %d ≠ 期望 %d", resp.StatusCode, expectStatus)
		return r
	}
	// 关键词断言（大小写不敏感）
	if keyword != "" && !strings.Contains(strings.ToLower(string(body)), strings.ToLower(keyword)) {
		r.Err = fmt.Sprintf("响应内容中未找到关键词「%s」", keyword)
		return r
	}

	r.OK = true
	return r
}

// ProbeTCP 对 host:port 执行一次 TCP 连接探测（用于数据库 / API 端口等）
func ProbeTCP(addr string, timeoutSec int) Result {
	start := time.Now()
	r := Result{}
	conn, err := net.DialTimeout("tcp", addr, time.Duration(timeoutSec)*time.Second)
	if err != nil {
		r.MS = int(time.Since(start).Milliseconds())
		r.Err = prettifyNetErr(err.Error())
		return r
	}
	_ = conn.Close()
	r.MS = int(time.Since(start).Milliseconds())
	r.OK = true
	return r
}

// prettifyNetErr 把常见网络错误翻译为可读中文
func prettifyNetErr(msg string) string {
	switch {
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝（服务未启动或端口未开放）"
	case strings.Contains(msg, "no such host"):
		return "域名解析失败（DNS 无法解析该域名）"
	case strings.Contains(msg, "connection reset"):
		return "连接被对端重置"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out"):
		return "连接超时（服务器无法在限定时间内建立连接）"
	case strings.Contains(msg, "certificate is expired"):
		return "SSL 证书已过期"
	case strings.Contains(msg, "certificate signed by unknown authority"):
		return "SSL 证书不被信任（可能是自签名证书）"
	case strings.Contains(msg, "x509:"):
		return "SSL/TLS 证书错误: " + msg
	case strings.Contains(msg, "EOF"):
		return "连接被服务端意外关闭（EOF）"
	}
	return msg
}
