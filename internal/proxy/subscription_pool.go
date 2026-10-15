// Package proxy - Codex 订阅代理：池模式适配器
// 创建时间: 2026-09-10
//
// 池模式只作用于精确的 POST /v1/responses。本适配器是唯一的客户端头过滤点：
// 正文先按旧入口规则规范化（含 zstd 解压），再**从空集合**只保留已校验的
// Content-Type / Accept，然后交给 handler.ServeCodexPoolRequest 进入现有账号池链路。
//
// 不能先把原始请求交给旧 helper 再补删头：copyRequestHeadersForAccount 是黑名单复制，
// 会带走客户端账号 ID、attestation、线程关联与 Codex metadata。
// 客户端关联信息在 subscription_handler 中已提取为有界内部 context，此处不再保留其请求头。
package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"cc-forwarder/config"
)

const (
	subscriptionRequestSource  = "codex_subscription_proxy"
	subscriptionGenerationKind = "generation"
)

type subscriptionPoolMetadata struct {
	RequestSource string
	RequestKind   string
	BodyMetadata  *normalizedRequestBodyMetadata
	BodyError     *requestBodyError
	// ReceivedAt 适配器收到请求的时刻；正文在进入 Handler 前已读取，生命周期起点需前移到这里，
	// 与直通模式一致地把读取、解压与 JSON 校验计入本地处理。
	ReceivedAt time.Time
}

type subscriptionPoolMetadataKey struct{}

// withSubscriptionPoolMetadata 是 P2b lifecycle_manager/handler 的唯一 context 接缝。
// 本适配器不创建生命周期、不创建 PrivacyRequestState，也不扫描正文。
func withSubscriptionPoolMetadata(ctx context.Context) context.Context {
	return context.WithValue(ctx, subscriptionPoolMetadataKey{}, subscriptionPoolMetadata{
		RequestSource: subscriptionRequestSource,
		RequestKind:   subscriptionGenerationKind,
	})
}

func subscriptionPoolMetadataFromContext(ctx context.Context) (subscriptionPoolMetadata, bool) {
	if ctx == nil {
		return subscriptionPoolMetadata{}, false
	}
	metadata, ok := ctx.Value(subscriptionPoolMetadataKey{}).(subscriptionPoolMetadata)
	return metadata, ok
}

// subscriptionPoolAdapter 池模式请求适配器。
type subscriptionPoolAdapter struct {
	proxy  *Handler
	logger *slog.Logger
}

// newSubscriptionPoolAdapter 创建池模式适配器。
func newSubscriptionPoolAdapter(proxy *Handler, logger *slog.Logger) *subscriptionPoolAdapter {
	return &subscriptionPoolAdapter{proxy: proxy, logger: logger}
}

// serve 隔离客户端头后转入现有账号池链路。
func (a *subscriptionPoolAdapter) serve(w http.ResponseWriter, r *http.Request, route *subscriptionRoute, requestID string) {
	receivedAt := time.Now()
	if route == nil || route.Kind != subscriptionRouteResponses || r.Method != http.MethodPost {
		writeAccountPipelineError(w, http.StatusNotFound, "codex_subscription_operation_not_supported",
			"the Codex subscription proxy only routes POST /v1/responses to the account pool")
		return
	}
	if a.proxy == nil {
		writeAccountPipelineError(w, http.StatusServiceUnavailable, "account_pool_unavailable",
			"account pool service is not initialized")
		return
	}

	// 正文规范化必须在剥离 Content-Encoding 之前完成：之后重建的头里不再有该字段。
	// 原 Handler 消费该结果并独占失败记录，避免重复读取、丢失压缩元数据或漏记失败。
	body, bodyErr := normalizeRequestBodyWithMetadata(w, r, a.config())
	if bodyErr == nil && body != nil {
		if err := validateSubscriptionJSON(body.normalizedBody); err != nil {
			bodyErr = &requestBodyError{StatusCode: http.StatusBadRequest, Code: subscriptionInvalidJSONCode,
				Message: "request contains invalid or ambiguous JSON"}
		}
	}

	association := subscriptionRequestAssociation(r)
	a.logger.Debug("🔀 [Codex 订阅代理] [池模式] 客户端头已隔离，转入账号池",
		"conn_id", requestID, "thread_id", association.ThreadID, "originator", association.Originator)

	// 就地重建：后续 copyRequestHeadersForAccount 只能看到这两项，客户端身份头无法回流。
	source := captureCodexIdentitySource(r)
	*r = *r.WithContext(context.WithValue(r.Context(), codexIdentitySourceKey{}, source))
	r.Header = buildSubscriptionPoolHeaders(r.Header)
	*r = *r.WithContext(withSubscriptionPoolMetadata(r.Context()))
	metadata, _ := subscriptionPoolMetadataFromContext(r.Context())
	metadata.BodyMetadata, metadata.BodyError, metadata.ReceivedAt = body, bodyErr, receivedAt
	*r = *r.WithContext(context.WithValue(r.Context(), subscriptionPoolMetadataKey{}, metadata))
	a.proxy.ServeCodexPoolRequest(w, r)
}

// subscriptionRequestAssociation 读取 handler 已提取的关联信息；缺失时返回零值。
func subscriptionRequestAssociation(r *http.Request) subscriptionClientAssociation {
	if info := subscriptionRequestInfoFromContext(r.Context()); info != nil {
		return info.Association
	}
	return subscriptionClientAssociation{}
}

func (a *subscriptionPoolAdapter) config() *config.Config {
	if a.proxy == nil {
		return nil
	}
	return a.proxy.config
}
