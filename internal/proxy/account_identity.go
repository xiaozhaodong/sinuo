package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/codexidentity"
	"cc-forwarder/internal/store"
)

// CodexIdentityProvider 返回单一 settings 权威来源的请求策略快照。
type CodexIdentityProvider interface {
	CodexIdentityPolicy(context.Context) (codexidentity.Policy, error)
}
type codexIdentitySnapshotProvider interface {
	CodexIdentityPolicySnapshot(context.Context) (codexidentity.Policy, error)
}
type codexIdentityRequestKey struct{}
type codexIdentitySourceKey struct{}

func (h *Handler) SetCodexIdentityProvider(provider CodexIdentityProvider) {
	h.codexIdentityProvider = provider
}

// captureCodexIdentitySource 先执行第一批隔离，避免 Connection 点名字段经 context 回流。
func captureCodexIdentitySource(r *http.Request) codexidentity.SourceHeaders {
	clean := &http.Request{URL: r.URL, Header: make(http.Header)}
	copyRequestHeadersForAccount(r, clean)
	return codexidentity.CaptureSourceHeaders(clean.Header)
}
func (h *Handler) attachCodexIdentityRequest(ctx context.Context, r *http.Request) (context.Context, error) {
	if snapshot, ok := r.Context().Value(codexIdentityRequestKey{}).(*codexidentity.Request); ok {
		return context.WithValue(ctx, codexIdentityRequestKey{}, snapshot), nil
	}
	policy := codexidentity.Policy{}
	if h.codexIdentityProvider != nil {
		var err error
		if snapshotProvider, ok := h.codexIdentityProvider.(codexIdentitySnapshotProvider); ok {
			policy, err = snapshotProvider.CodexIdentityPolicySnapshot(ctx)
		} else {
			policy, err = h.codexIdentityProvider.CodexIdentityPolicy(ctx)
		}
		if err != nil {
			return ctx, &codexidentity.Error{Code: codexidentity.CodeSettingsUnavailable, Message: "Codex 身份设置读取失败", Status: 503}
		}
	}
	source, ok := r.Context().Value(codexIdentitySourceKey{}).(codexidentity.SourceHeaders)
	if !ok {
		source = captureCodexIdentitySource(r)
	}
	ctx = context.WithValue(ctx, codexIdentityRequestKey{}, codexidentity.NewRequest(policy, source))
	*r = *r.WithContext(ctx)
	return ctx, nil
}
func identityRequest(r *http.Request) *codexidentity.Request {
	value, _ := r.Context().Value(codexIdentityRequestKey{}).(*codexidentity.Request)
	return value
}
func identityOperation(path string) codexidentity.Operation {
	switch path {
	case "/v1/responses":
		return codexidentity.OperationResponses
	case "/v1/responses/compact":
		return codexidentity.OperationCompact
	case "/v1/models":
		return codexidentity.OperationModels
	case codexStandaloneSearchPath:
		return codexidentity.OperationSearch
	}
	return codexidentity.OperationOther
}
func codexIdentityError(err error) *codexidentity.Error {
	var e *codexidentity.Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

func (h *Handler) identityOwners() *codexidentity.OwnerIndex {
	h.codexIdentityOnce.Do(func() { h.codexIdentityOwners = codexidentity.NewOwnerIndex(4096, 30*time.Minute) })
	return h.codexIdentityOwners
}

// 共享响应边界：普通、SSE、错误分流前执行，首次 WriteHeader/Flush 即可安全发起下一轮。
func (h *Handler) observeAccountIdentityResponse(resp *http.Response, prepared *codexidentity.Prepared) {
	if resp == nil {
		return
	}
	named := connectionNamedHeaders(resp.Header)
	for key := range resp.Header {
		lower := strings.ToLower(key)
		_, drop := named[lower]
		if drop || lower == "set-cookie" || lower == "trailer" || lower == "connection" || lower == "keep-alive" || lower == "proxy-authenticate" || lower == "proxy-authorization" || lower == "te" || lower == "transfer-encoding" || lower == "upgrade" {
			delete(resp.Header, key)
		}
	}
	if prepared == nil {
		return
	}
	if !prepared.IdentityEnabled {
		return
	}
	if !prepared.FirstParty {
		resp.Header.Del(codexidentity.HeaderTurnState)
		return
	}
	if prepared.Owner != (codexidentity.Owner{}) {
		h.identityOwners().Register(resp.Header.Get(codexidentity.HeaderTurnState), prepared.Owner, time.Now())
	}
}

var identityProfileWarning sync.Map

func logIdentityProfile(prepared *codexidentity.Prepared, policy codexidentity.Policy) {
	if prepared == nil || !prepared.FirstParty {
		return
	}
	profile, err := codexidentity.BuiltinProfile(policy.Version)
	if err == nil && prepared.DescriptionSource == "builtin" && profile.IsStale(time.Now()) {
		if _, loaded := identityProfileWarning.LoadOrStore(policy.Version, true); !loaded {
			reason := "expired"
			if profile.ValidatedAt.IsZero() {
				reason = "unvalidated_version"
			}
			slog.Warn("Codex 内置身份档案待复验", "version", policy.Version, "source", "builtin", "reason", reason)
		}
	}
}

const (
	identityExtensionWarnInterval = time.Hour
	// 名称来自客户端，限制记录表大小，满了整体清空即可（最坏只是多告警一次）。
	identityExtensionWarnCapacity = 256
)

var identityExtensionWarnings = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func allowIdentityExtensionWarning(key string, now time.Time) bool {
	identityExtensionWarnings.Lock()
	defer identityExtensionWarnings.Unlock()
	if last, ok := identityExtensionWarnings.last[key]; ok && now.Sub(last) < identityExtensionWarnInterval {
		return false
	}
	if len(identityExtensionWarnings.last) >= identityExtensionWarnCapacity {
		clear(identityExtensionWarnings.last)
	}
	identityExtensionWarnings.last[key] = now
	return true
}

// warnIdentityExtensions 限频提示透传的未验证 beta 值和被删除的未知首方扩展字段，只记录名称。
// 两者通常意味着客户端升级引入了新协议，需要复核规则。
func warnIdentityExtensions(prepared *codexidentity.Prepared) {
	now := time.Now()
	for _, feature := range prepared.UnknownBetaFeatures {
		if allowIdentityExtensionWarning("beta:"+feature, now) {
			slog.Warn("Codex 客户端声明了未验证的 beta 功能，已原样透传，由上游决定是否支持", "feature", feature)
		}
	}
	for _, field := range prepared.DroppedFields {
		if allowIdentityExtensionWarning("drop:"+field, now) {
			slog.Warn("Codex 请求含未识别的首方扩展字段，已删除以免未映射的身份信息出站；若相关功能异常需补充映射规则", "field", field)
		}
	}
}

// identityDecision 是单次 attempt 身份处理结果的日志输入，只读 Prepare 已算好的值。
type identityDecision struct {
	identity   *codexidentity.Request
	prepared   *codexidentity.Prepared
	scope      codexidentity.AccountScope
	operation  codexidentity.Operation
	sourceBody []byte // Prepare 前的正文，用于判断身份层是否改写了正文
}

// identityEffect 返回本次身份处理的短码与日志文案。
func identityEffect(prepared *codexidentity.Prepared) (string, string) {
	switch {
	case !prepared.IdentityEnabled:
		return "passthrough", "🪪 [Codex 身份] 未生效，原样透传"
	case prepared.FirstParty:
		return "mapped", "🪪 [Codex 身份] 已按账号映射身份（官方 OAuth）"
	default:
		return "third_party_cleanup", "🪪 [Codex 身份] 已清理首方身份字段（强制开启，第三方目标）"
	}
}

func identityTurnStateAction(identity *codexidentity.Request, prepared *codexidentity.Prepared) string {
	switch {
	case identity.Source.TurnState == "":
		return "absent"
	case !prepared.IdentityEnabled:
		return "passthrough"
	case prepared.Headers.Get(codexidentity.HeaderTurnState) != "":
		return "kept_owner_match"
	}
	return "dropped_owner_mismatch_or_unknown"
}

// logIdentityDecision 按 request_id 记录每个 attempt 身份层实际做了什么，只记录头名不记录值。
// Debug 未开启时直接返回，避免每次 attempt 白做正文比较和头名排序。
func logIdentityDecision(ctx context.Context, acc *store.UpstreamAccountRecord, d identityDecision) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return
	}
	effect, message := identityEffect(d.prepared)
	attrs := []any{
		"request_id", failoverRequestID(ctx), "account_id", acc.ID, "account", acc.AccountName,
		"operation", d.operation, "target_host", d.scope.TargetHost(), "mode", d.identity.Policy.Mode(),
		"effective", d.prepared.IdentityEnabled, "first_party", d.prepared.FirstParty, "effect", effect,
		"turn_state_action", identityTurnStateAction(d.identity, d.prepared),
	}
	if d.prepared.IdentityEnabled {
		if d.prepared.FirstParty {
			attrs = append(attrs,
				"ua_source", d.prepared.DescriptionSource, "version", d.prepared.Version, "rule", codexidentity.ProfileRuleVersion)
		}
		attrs = append(attrs,
			"set_headers", slices.Sorted(maps.Keys(d.prepared.Headers)),
			"body_rewritten", !bytes.Equal(d.sourceBody, d.prepared.Body),
			"dropped_fields", d.prepared.DroppedFields)
	}
	slog.Debug(message, attrs...)
}

// 每个 attempt 从调用方保存的原始正文计算，凭据、目标 scope 和隐私后校验在此收敛。
func (h *Handler) prepareAccountIdentity(ctx context.Context, r *http.Request, bodyBytes []byte, acc *store.UpstreamAccountRecord, targetURL string) ([]byte, http.Header, *codexidentity.Prepared, error) {
	identity := identityRequest(r)
	accountID, authHeaders, err := h.resolveAccountScopeID(ctx, acc)
	if err != nil {
		return nil, nil, nil, err
	}
	scope, scopeErr := codexidentity.NewAccountScope(acc.ID, accountauth.NormalizeProviderType(acc.ProviderType), targetURL, accountID, accountauth.IsChatGPTOAuthProvider(acc.ProviderType))
	if scopeErr != nil {
		return nil, nil, nil, scopeErr
	}
	operation := identityOperation(r.URL.Path)
	prepared, err := identity.Prepare(scope, operation, bodyBytes, h.identityOwners())
	if err != nil {
		return nil, nil, nil, err
	}
	logIdentityDecision(ctx, acc, identityDecision{
		identity: identity, prepared: prepared, scope: scope, operation: operation, sourceBody: bodyBytes,
	})
	bodyBytes = prepared.Body
	if prepared.IdentityEnabled {
		logIdentityProfile(prepared, identity.Policy)
		warnIdentityExtensions(prepared)
	}

	// 🛡️ 出站隐私过滤（PolicyError 由 handleAccountPipeline 短路，不冷却、不换号）
	bodyBytes, err = h.applyAccountPrivacyFilter(r, bodyBytes, acc)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := prepared.ValidatePrivacyBody(bodyBytes); err != nil {
		return nil, nil, nil, err
	}
	if authHeaders == nil {
		if authHeaders, err = h.resolveAccountAuthHeaders(ctx, acc); err != nil {
			return nil, nil, nil, err
		}
	}
	// scope 与出站头绑定同一账号；按 RT 缓存的 ID 可能落后于已编辑的凭据，以本地凭据为准。
	if accountID != "" {
		authHeaders.Set("chatgpt-account-id", accountID)
	}
	return bodyBytes, authHeaders, prepared, nil
}

// resolveAccountScopeID 返回派生 scope 的真实账号 ID。优先读本地凭据，让隐私过滤先于
// 可能联网的 RT 刷新；本地缺失时才提前解析凭据，返回的头供出站复用，避免重复解析。
func (h *Handler) resolveAccountScopeID(ctx context.Context, acc *store.UpstreamAccountRecord) (string, http.Header, error) {
	if !accountauth.IsChatGPTOAuthProvider(acc.ProviderType) {
		return "", nil, nil
	}
	if accountID := accountauth.ExtractChatGPTAccountID(acc.CredentialRaw); accountID != "" {
		return accountID, nil, nil
	}
	authHeaders, err := h.resolveAccountAuthHeaders(ctx, acc)
	if err != nil {
		return "", nil, err
	}
	accountID := authHeaders.Get("chatgpt-account-id")
	if accountID == "" {
		return "", nil, fmt.Errorf("chatgpt_account_id is missing from credential")
	}
	return accountID, authHeaders, nil
}

// resolveAccountAuthHeaders 解析出站鉴权头；OAuth 账号缓存未命中时会触发 RT 刷新。
func (h *Handler) resolveAccountAuthHeaders(ctx context.Context, acc *store.UpstreamAccountRecord) (http.Header, error) {
	authRequest := &http.Request{Header: make(http.Header)}
	if err := accountauth.ApplyAccountAuth(ctx, authRequest, acc.ProviderType, acc.CredentialRaw, h.refreshTokenManager); err != nil {
		return nil, fmt.Errorf("failed to apply account auth: %w", err)
	}
	return authRequest.Header, nil
}

// 订阅进池在选账号前已隔离来源头；自动选中第三方时，仅恢复已校验的会话状态。
// 凭据、Cookie、attestation 及 Connection 点名的头从不通过此处回流。
func restoreAutomaticThirdPartyState(r *http.Request, req *http.Request, prepared *codexidentity.Prepared) {
	identity := identityRequest(r)
	if identity == nil || identity.Policy.Mode() != codexidentity.IdentityModeAuto || prepared.IdentityEnabled {
		return
	}
	if _, subscription := subscriptionPoolMetadataFromContext(r.Context()); subscription && identity.Source.TurnState != "" {
		req.Header.Set(codexidentity.HeaderTurnState, identity.Source.TurnState)
	}
}

// 映射绑定了目标 origin；跨 origin 重定向直接失败，不能带原身份正文跟随。
// 复用 Transport/超时，在单次请求 client 副本上加边界，不改变共享工厂。
func identityBoundClient(client *http.Client) *http.Client {
	bound := *client
	bound.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && (!strings.EqualFold(req.URL.Scheme, via[0].URL.Scheme) || !strings.EqualFold(req.URL.Host, via[0].URL.Host)) {
			return &codexidentity.Error{Code: codexidentity.CodeRedirectOriginChanged, Message: "Codex 身份请求不允许跨目标 origin 重定向", Status: http.StatusBadGateway}
		}
		if client.CheckRedirect != nil {
			return client.CheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &bound
}
