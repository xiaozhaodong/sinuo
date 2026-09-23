package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// 账号池出站请求头隔离（P1 批次 1，始终生效，不受身份补全开关影响）。
//
// 三类来源头不得进入上游：
//   - 固定 hop-by-hop 与正文传输头：由代理按最终 wire bytes 与连接自行生成；
//   - Connection 点名的动态 hop-by-hop 头：大小写不敏感、覆盖多个 Connection 值；
//   - 来源凭据与代理来源信息：Authorization / Cookie / x-api-key / Proxy-Authorization、
//     Forwarded / X-Forwarded-* / Via，目标凭据只由账号模块按账号契约应用。
//
// 先剥离再重建：被点名的头即便也是目标需要的（如 `Connection: Authorization`），
// 也只影响来源值的复制，不影响之后由账号模块生成的目标值。

var accountRequestFixedSkipHeaders = map[string]struct{}{
	// hop-by-hop（RFC 9110 §7.6.1）与正文传输相关
	"connection": {}, "keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {},
	"te": {}, "trailer": {}, "trailers": {}, "transfer-encoding": {}, "upgrade": {},
	"content-encoding": {}, "content-length": {}, "host": {},
	// 来源凭据
	"authorization": {}, "cookie": {}, "x-api-key": {}, "chatgpt-account-id": {}, "x-oai-attestation": {},
	// 代理来源信息（不属于 hop-by-hop，但同样不能透传到第一方后端）
	"forwarded": {}, "via": {},
}

// connectionNamedHeaders 解析所有 Connection 值点名的头名（小写）。
func connectionNamedHeaders(header http.Header) map[string]struct{} {
	named := map[string]struct{}{}
	for key, values := range header {
		if !strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				token = strings.ToLower(strings.TrimSpace(token))
				if token != "" {
					named[token] = struct{}{}
				}
			}
		}
	}
	return named
}

// shouldSkipAccountRequestHeader 判断来源头是否不得复制到账号池上游请求。
func shouldSkipAccountRequestHeader(key string, connectionNamed map[string]struct{}) bool {
	lower := strings.ToLower(key)
	if _, fixed := accountRequestFixedSkipHeaders[lower]; fixed {
		return true
	}
	if strings.HasPrefix(lower, "x-forwarded-") {
		return true
	}
	_, named := connectionNamed[lower]
	return named
}

// copyRequestHeadersForAccount 按隔离规则复制来源头，并按目标 URL 重建 Host。
// Content-Encoding / Content-Length 与目标凭据由调用方在此之后按最终正文与账号契约设置。
func copyRequestHeadersForAccount(src *http.Request, dst *http.Request) {
	if src == nil || dst == nil {
		return
	}
	connectionNamed := connectionNamedHeaders(src.Header)
	for key, values := range src.Header {
		if shouldSkipAccountRequestHeader(key, connectionNamed) {
			continue
		}
		for _, value := range values {
			dst.Header.Add(key, value)
		}
	}
	if u, err := url.Parse(dst.URL.String()); err == nil {
		dst.Header.Set("Host", u.Host)
		dst.Host = u.Host
	}
}

// accountResponseSkipHeaders 账号池 SSE 响应头复制时不回传的头：
// hop-by-hop，以及解压后不再成立的 Content-Encoding / Content-Length。
func shouldSkipAccountResponseHeader(key string, connectionNamed map[string]struct{}) bool {
	lower := strings.ToLower(key)
	if isHopByHopHeader(lower) || lower == "content-encoding" {
		return true
	}
	_, named := connectionNamed[lower]
	return named
}
