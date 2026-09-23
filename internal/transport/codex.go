package transport

import (
	"fmt"
	"net/http"
	"time"

	"cc-forwarder/config"
)

// NewCodexHTTPClient 构造发往 ChatGPT / Codex 后端及其 OAuth 接口的 HTTP client。
//
// 统一规则：
//   - 继承 cfg 中的 HTTP / HTTPS / SOCKS5 代理配置；cfg 为 nil 时克隆 DefaultTransport，
//     保留环境变量代理语义，不解引用 nil 配置。
//   - 关闭 Go 的自动 gzip 协商：Codex 原生客户端不发送 Accept-Encoding（P0 2026-09-22 实测），
//     自动补 gzip 会让辅助请求与账号池 / 订阅直通的出站形态不一致。
//   - 不共享连接池：辅助请求各自的超时与重定向语义由调用方保留。
func NewCodexHTTPClient(cfg *config.Config, timeout time.Duration) (*http.Client, error) {
	var httpTransport *http.Transport
	if cfg == nil {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("http.DefaultTransport is not *http.Transport")
		}
		httpTransport = base.Clone()
	} else {
		created, err := CreateTransport(cfg)
		if err != nil {
			return nil, err
		}
		httpTransport = created
	}
	ApplyCodexTransportPolicy(httpTransport)
	return &http.Client{
		Timeout:   timeout,
		Transport: httpTransport,
	}, nil
}

// ApplyCodexTransportPolicy 对已存在的 transport 应用 Codex 出站策略，供自建连接池的调用方复用。
func ApplyCodexTransportPolicy(httpTransport *http.Transport) {
	if httpTransport == nil {
		return
	}
	httpTransport.DisableCompression = true
}
