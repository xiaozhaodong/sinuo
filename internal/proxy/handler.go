package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/codexidentity"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/events"
	"cc-forwarder/internal/middleware"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/proxy/response"
	"cc-forwarder/internal/reasoning"
	"cc-forwarder/internal/tracking"
)

// Context key for endpoint information
type contextKey string

const EndpointContextKey = contextKey("endpoint")

// Handler handles HTTP proxy requests
type Handler struct {
	endpointManager               *endpoint.Manager
	config                        *config.Config
	usageTracker                  *tracking.UsageTracker
	codexIdentityProvider         CodexIdentityProvider
	codexIdentityOnce             sync.Once
	codexIdentityOwners           *codexidentity.OwnerIndex
	accountPoolService            AccountPoolService
	codexModelRouter              CodexModelRouter
	codexModelsProvider           CodexModelListProvider
	claudeModelsProvider          ClaudeModelListProvider
	imageGenerationConfigProvider ImageGenerationConfigProvider
	monitoringMiddleware          *middleware.MonitoringMiddleware
	responseProcessor             *response.Processor
	tokenAnalyzer                 *response.TokenAnalyzer
	forwarder                     *handlers.Forwarder
	refreshTokenManager           *accountauth.OpenAIRefreshTokenManager
	eventBus                      events.EventBus // EventBus事件总线
	accountHTTPInitOnce           sync.Once
	accountHTTPInitErr            error
	accountHTTPTransport          *http.Transport
	accountHTTPClient             *http.Client
	accountSSEHTTPClient          *http.Client
	imageHTTPInitOnce             sync.Once
	imageHTTPInitErr              error
	imageHTTPTransport            *http.Transport
	imageHTTPClient               *http.Client
	imageDirectHTTPClientFactory  func(int, int) (*http.Client, func(), error)
	// 🛡️ 出站隐私过滤（可选注入，nil 时不影响原有链路）
	privacyFilter handlers.PrivacyFilter
	// 故障转移通知回调仅用于向桌面/前端推送结构化事件，不改变调度语义。
	failoverNotifierMu  sync.RWMutex
	onFailoverTriggered func(FailoverEvent)
	// §9.2 规则 5：endpoint+scope 非阻塞 429 重试 gate，同一时刻只放行一个延迟重试
	rateLimitRetryGates   sync.Map
	endpointDispatchPacer *endpointDispatchPacer
	// claudeSubscriptionBaseURL Claude Code 订阅直通的上游；空值为 Anthropic 官方地址，仅测试注入本地上游
	claudeSubscriptionBaseURL string
	// claudeQuota 订阅直通的官方额度（只在内存）：响应头采集、请求结束后补查与查询凭据
	claudeQuota *claudeSubscriptionQuota
}

type CodexModelListProvider interface {
	GetCodexModelListResponse(ctx context.Context) ([]byte, bool, error)
}

type ClaudeModelListProvider interface {
	GetClaudeModelListResponse(ctx context.Context) ([]byte, error)
}

// ImageGenerationConfigProvider 提供独立图像生成上游配置。
type ImageGenerationConfigProvider interface {
	GetImageGenerationConfig(ctx context.Context) (ImageGenerationConfig, error)
}

// TokenParserProviderImpl 实现TokenParserProvider接口
type TokenParserProviderImpl struct{}

// NewTokenParser 创建新的TokenParser实例
func (p *TokenParserProviderImpl) NewTokenParser() response.TokenParser {
	return NewTokenParser()
}

// NewTokenParserWithUsageTracker 创建带有UsageTracker的TokenParser实例
func (p *TokenParserProviderImpl) NewTokenParserWithUsageTracker(requestID string, usageTracker *tracking.UsageTracker) response.TokenParser {
	return NewTokenParserWithUsageTracker(requestID, usageTracker)
}

// NewHandler creates a new proxy handler
func NewHandler(endpointManager *endpoint.Manager, cfg *config.Config) *Handler {
	// 创建forwarder
	forwarder := handlers.NewForwarder(cfg, endpointManager)

	h := &Handler{
		endpointManager:       endpointManager,
		config:                cfg,
		responseProcessor:     response.NewProcessor(),
		forwarder:             forwarder,
		refreshTokenManager:   accountauth.NewOpenAIRefreshTokenManager(cfg),
		endpointDispatchPacer: newEndpointDispatchPacer(temporaryVenlacyMinDispatchInterval),
	}
	h.claudeQuota = newClaudeSubscriptionQuota(h.fetchClaudeSubscriptionUsage, defaultClaudeUsagePolicy)

	// 初始化 token analyzer
	provider := &TokenParserProviderImpl{}
	h.tokenAnalyzer = response.NewTokenAnalyzer(nil, nil, provider)

	return h
}

// SetMonitoringMiddleware 设置监控中间件用于重试跟踪
func (h *Handler) SetMonitoringMiddleware(mm *middleware.MonitoringMiddleware) {
	h.monitoringMiddleware = mm

	// 同时更新tokenAnalyzer的monitoringMiddleware
	if h.tokenAnalyzer != nil {
		provider := &TokenParserProviderImpl{}
		h.tokenAnalyzer = response.NewTokenAnalyzer(h.usageTracker, mm, provider)
	}
}

// monitoringMiddlewareForAnalyzer 返回用于 TokenAnalyzer 的监控中间件。
// 未设置时返回纯 nil interface，避免 typed-nil 指针通过类型断言后被调用。
func (h *Handler) monitoringMiddlewareForAnalyzer() any {
	if h.monitoringMiddleware == nil {
		return nil
	}
	return h.monitoringMiddleware
}

// SetUsageTracker sets the usage tracker for request tracking
func (h *Handler) SetUsageTracker(ut *tracking.UsageTracker) {
	h.usageTracker = ut

	provider := &TokenParserProviderImpl{}
	h.tokenAnalyzer = response.NewTokenAnalyzer(ut, h.monitoringMiddlewareForAnalyzer(), provider)
}

// SetEventBus 设置EventBus事件总线
func (h *Handler) SetEventBus(eventBus events.EventBus) {
	h.eventBus = eventBus
}

// SetAccountPoolService 设置账号池服务
func (h *Handler) SetAccountPoolService(service AccountPoolService) {
	h.accountPoolService = service
}

// SetCodexModelRouter 注入独立的 Codex 模型路由适配器。
func (h *Handler) SetCodexModelRouter(router CodexModelRouter) {
	h.codexModelRouter = router
}

// SetPrivacyFilter 注入出站隐私过滤依赖，并传递给 endpoint 常规/流式与账号池链路。
// 必须在 SetUsageTracker 之后保持有效（SetUsageTracker 重建 handler 时会重新注入）。
func (h *Handler) SetPrivacyFilter(filter handlers.PrivacyFilter) {
	wrapped := filter
	if filter != nil {
		if _, ok := filter.(subscriptionPrivacyFilter); !ok {
			wrapped = subscriptionPrivacyFilter{inner: filter}
		}
	}
	h.privacyFilter = wrapped
	if h.forwarder != nil {
		h.forwarder.SetPrivacyFilter(wrapped)
	}
}

// SetCodexModelListProvider 设置本地 Codex 模型目录提供器。
func (h *Handler) SetCodexModelListProvider(provider CodexModelListProvider) {
	h.codexModelsProvider = provider
}

// SetClaudeModelListProvider 设置 Claude Code Gateway Discovery 的本地展示目录提供器。
func (h *Handler) SetClaudeModelListProvider(provider ClaudeModelListProvider) {
	h.claudeModelsProvider = provider
}

// SetImageGenerationConfigProvider 设置独立图像生成配置提供器。
func (h *Handler) SetImageGenerationConfigProvider(provider ImageGenerationConfigProvider) {
	h.imageGenerationConfigProvider = provider
}

// extractModelFromRequestBody 从请求体中提取模型名称
// 仅对已知会携带 model 字段的路径进行解析，避免不必要的JSON解析开销
func (h *Handler) extractModelFromRequestBody(bodyBytes []byte, path string) string {
	// 仅对已知携带 model 的请求尝试解析
	if !strings.Contains(path, "/v1/messages") && !h.isAccountPipelinePath(path) && path != openAIImagesGenerationsPath {
		return ""
	}

	// 避免解析空请求体
	if len(bodyBytes) == 0 {
		return ""
	}

	var requestBody struct {
		Model string `json:"model"`
	}

	if err := json.Unmarshal(bodyBytes, &requestBody); err == nil && requestBody.Model != "" {
		return requestBody.Model
	}

	return ""
}

// ServeHTTP implements the http.Handler interface
// 统一请求分发逻辑 - 整合流式处理、错误恢复和生命周期管理
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	connID := ""
	if connIDValue, ok := r.Context().Value("conn_id").(string); ok {
		connID = connIDValue
	}

	if h.isClaudeModelsRequest(r) {
		h.handleClaudeModelsRequest(ctx, w)
		return
	}

	if h.isCodexModelsRequest(r) {
		h.handleCodexModelsRequest(ctx, w, r)
		return
	}

	// Claude Code 订阅直通不进端点调度；count_tokens 也直接交给官方，不做本地估算
	claudeSubscription := endpoint.IsClaudeCodeSubscription(r.Context())
	countTokensIntercept := r.URL.Path == "/v1/messages/count_tokens" && h.config.TokenCounting.Enabled && !claudeSubscription
	poolMetadata, hasPoolMetadata := subscriptionPoolMetadataFromContext(r.Context())
	var lifecycleManager *RequestLifecycleManager
	startTrackedRequest := func(bool) {}
	if !countTokensIntercept {
		// 创建统一的请求生命周期管理器
		lifecycleManager = NewRequestLifecycleManager(h.usageTracker, h.monitoringMiddleware, connID, h.eventBus)
		if hasPoolMetadata {
			lifecycleManager.SetRequestSource(poolMetadata.RequestSource)
			lifecycleManager.SetRequestKind(poolMetadata.RequestKind)
		}
		switch {
		case h.isAccountPipelinePath(r.URL.Path):
			lifecycleManager.SetRequestFamily(tracking.RequestFamilyCodex)
		case isImageAPIPath(r.URL.Path):
			lifecycleManager.SetRequestFamily(tracking.RequestFamilyImage)
		case r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/messages/count_tokens":
			lifecycleManager.SetRequestFamily(tracking.RequestFamilyClaude)
		default:
			lifecycleManager.SetRequestFamily(tracking.RequestFamilyOther)
		}
		// Codex 账号池、图像代理与 Claude 订阅直通都不挂载 endpoint 失败追踪语义
		if !h.isAccountPipelinePath(r.URL.Path) && !isImageAPIPath(r.URL.Path) && !claudeSubscription {
			// 📊 [失败追踪] 设置端点管理器，用于记录成功/失败
			lifecycleManager.SetEndpointManager(h.endpointManager)
		}

		clientIP := r.RemoteAddr
		userAgent := r.Header.Get("User-Agent")
		requestStarted := false
		startTrackedRequest = func(isStreaming bool) {
			if requestStarted {
				return
			}
			lifecycleManager.StartRequest(clientIP, userAgent, r.Method, r.URL.Path, isStreaming)
			requestStarted = true
		}
	}

	// 请求方向唯一规范化入口：读取一次，必要时解压 zstd，并清理压缩元数据。
	var normalizedBodyMetadata *normalizedRequestBodyMetadata
	var bodyErr *requestBodyError
	if hasPoolMetadata && (poolMetadata.BodyMetadata != nil || poolMetadata.BodyError != nil) {
		normalizedBodyMetadata, bodyErr = poolMetadata.BodyMetadata, poolMetadata.BodyError
	} else {
		normalizedBodyMetadata, bodyErr = normalizeRequestBodyWithMetadata(w, r, h.config)
	}
	if bodyErr != nil {
		if lifecycleManager != nil {
			startTrackedRequest(false)
			lifecycleManager.FailRequest(bodyErr.Code, bodyErr.Message, bodyErr.StatusCode)
		}
		writeRequestBodyError(w, r.URL.Path, bodyErr)
		return
	}
	bodyBytes := normalizedBodyMetadata.normalizedBody
	attachNormalizedRequestBodyMetadata(r, normalizedBodyMetadata)
	ctx = r.Context()

	// 🔢 [count_tokens拦截] 特殊处理count_tokens端点
	if countTokensIntercept {
		// 🛡️ count_tokens 走提前拦截分支，也必须挂载请求级隐私状态。
		// 这样同一请求在多个支持端点之间尝试时能复用过滤结果，并传递 requestID。
		if h.privacyFilter != nil {
			privacyState := handlers.NewPrivacyRequestState(connID)
			*r = *r.WithContext(handlers.WithPrivacyRequestState(r.Context(), privacyState))
			ctx = r.Context()
		}

		countTokensHandler := handlers.NewCountTokensHandler(h.config, h.endpointManager, h.forwarder)
		countTokensHandler.Handle(ctx, w, r, bodyBytes, connID)
		return
	}

	// 🛡️ 请求级隐私过滤状态：同一 requestID + scopeFingerprint 重试时复用扫描结果
	if h.privacyFilter != nil {
		reqID := lifecycleManager.GetRequestID()
		privacyState := handlers.NewPrivacyRequestState(reqID)
		// B4：只在主链路挂持久化 recorder。scanMu 串行化计数与对应持久化写，
		// 保证并发通知时 scan_count 与 last-write-wins 落库顺序一致。
		var scanMu sync.Mutex
		var scanCount int
		privacyState.SetResultRecorder(func(result privacy.ApplyResult, policyErr *privacy.PolicyError) {
			if h.usageTracker == nil || reqID == "" {
				return
			}
			scanMu.Lock()
			defer scanMu.Unlock()
			scanCount++
			if payload := handlers.BuildPrivacyScanJSON(result, policyErr, scanCount); payload != "" {
				h.usageTracker.RecordRequestUpdate(reqID, tracking.UpdateOptions{PrivacyScanJSON: &payload})
			}
		})
		*r = *r.WithContext(handlers.WithPrivacyRequestState(r.Context(), privacyState))
		ctx = r.Context()
	}

	// v6.3：同步解析请求模型与思考配置（一次 JSON 解码），早于 StartRequest，
	// 保证 422 未分配 / 账号池未就绪 / 秒终态等路径的 start 行也带请求字段。
	requestModel, requestReasoning := reasoning.ParseRequest(r.URL.Path, bodyBytes)
	if r.URL.Path == openAIImagesEditsPath {
		// 图片编辑允许 multipart/form-data，JSON 解析读不到其中的 model 字段；
		// 这里取默认模型注入前的原始值，避免显式模型被记成「默认模型」。
		requestModel = imageEditRequestModel(bodyBytes, r.Header.Get("Content-Type"), requestModel)
	}
	if requestModel != "" {
		lifecycleManager.SetModel(requestModel)
	}
	lifecycleManager.SetRequestProfile(requestModel, requestReasoning)

	// 检测是否为SSE流式请求
	isSSE := h.detectSSERequest(r, bodyBytes)

	// 开始请求跟踪（传递流式标记）
	startTrackedRequest(isSSE)

	if r.URL.Path == openAIImagesGenerationsPath {
		h.handleImageGeneration(ctx, w, r, bodyBytes, lifecycleManager)
		return
	}
	if r.URL.Path == openAIImagesEditsPath {
		h.handleImageEdit(ctx, w, r, bodyBytes, lifecycleManager)
		return
	}

	// Codex 账号池路径仅由账号池链路处理，不回退到 endpoint
	if h.shouldUseAccountPipeline(r.URL.Path) {
		h.handleAccountPipeline(ctx, w, r, bodyBytes, lifecycleManager)
		return
	}
	if h.isAccountPipelinePath(r.URL.Path) {
		h.handleUnavailableAccountPipeline(w, lifecycleManager)
		return
	}

	// Claude Code 订阅直通：直连 Anthropic 官方，不进端点调度
	if claudeSubscription {
		h.handleClaudeSubscription(ctx, w, r, bodyBytes, lifecycleManager, isSSE)
		return
	}

	// 统一端点转发管线（v7：streaming/regular 合并，仅 P2 处理分叉）
	h.handleEndpointPipeline(ctx, w, r, bodyBytes, lifecycleManager, isSSE)
}

// ServeCodexPoolRequest 供 Codex 订阅代理的池模式适配器调用。
// 只接受精确的 POST /v1/responses，其余路径与方法一律 404；请求头隔离由调用方完成，
// 这里只做入口守卫后交给既有 ServeHTTP 管线，旧入口行为不变。
func (h *Handler) ServeCodexPoolRequest(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil || r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
		writeAccountPipelineError(w, http.StatusNotFound, "codex_subscription_operation_not_supported",
			"the Codex subscription proxy only routes POST /v1/responses to the account pool")
		return
	}
	h.ServeHTTP(w, r)
}

func writeRequestBodyError(w http.ResponseWriter, path string, bodyErr *requestBodyError) {
	if bodyErr == nil {
		return
	}
	if isCodexAccountPoolRoute(path) {
		writeAccountPipelineError(w, bodyErr.StatusCode, bodyErr.Code, bodyErr.Message)
		return
	}
	http.Error(w, bodyErr.Message, bodyErr.StatusCode)
}

func (h *Handler) isCodexModelsRequest(r *http.Request) bool {
	return r != nil && r.Method == http.MethodGet && r.URL.Path == "/v1/models" && !h.isClaudeModelsRequest(r)
}

func (h *Handler) isClaudeModelsRequest(r *http.Request) bool {
	return r != nil && r.Method == http.MethodGet && r.URL.Path == "/v1/models" && strings.TrimSpace(r.Header.Get("anthropic-version")) != ""
}

func (h *Handler) handleClaudeModelsRequest(ctx context.Context, w http.ResponseWriter) {
	if h == nil || h.claudeModelsProvider == nil {
		writeClaudeModelsError(w, http.StatusServiceUnavailable, "claude_models_unavailable", "Claude model catalog is not available")
		return
	}

	responseBytes, err := h.claudeModelsProvider.GetClaudeModelListResponse(ctx)
	if err != nil {
		writeClaudeModelsError(w, http.StatusInternalServerError, "claude_models_error", "failed to load Claude model catalog")
		return
	}
	if len(responseBytes) == 0 {
		responseBytes = []byte(`{"data":[]}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(responseBytes)
}

func writeClaudeModelsError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
}

func (h *Handler) handleCodexModelsRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if h.tryWriteLocalCodexModels(ctx, w, r) {
		return
	}

	// /v1/models 是模型目录查询，不属于 token 消耗请求；穿透上游时也不写请求追踪表。
	if h.handleCodexModelsAccountPassthrough(ctx, w, r) {
		return
	}
	writeCodexModelsError(w, http.StatusServiceUnavailable, "codex_models_upstream_unavailable", "no account-pool upstream is available for Codex /v1/models")
}

func (h *Handler) handleCodexModelsAccountPassthrough(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.config == nil || !h.config.AccountPool.Enabled {
		return false
	}
	if h.accountPoolService == nil {
		writeCodexModelsError(w, http.StatusServiceUnavailable, "account_pool_unavailable", "account pool service is not initialized for Codex /v1/models")
		return true
	}

	var identityErr error
	ctx, identityErr = h.attachCodexIdentityRequest(ctx, r)
	if identityErr != nil {
		e := codexIdentityError(identityErr)
		writeCodexModelsError(w, e.Status, e.Code, e.Message)
		return true
	}

	preview, err := h.accountPoolService.PreviewSchedulableAccounts(ctx, r.URL.Path)
	if err != nil {
		writeCodexModelsError(w, http.StatusServiceUnavailable, "account_pool_unavailable", "failed to load schedulable account for Codex /v1/models")
		return true
	}
	accounts := preview.Accounts
	if len(accounts) == 0 {
		writeCodexModelsError(w, http.StatusServiceUnavailable, "account_pool_unavailable", "no schedulable account for Codex /v1/models")
		return true
	}

	requestID := failoverRequestID(ctx)
	if requestID == "" {
		requestID = newFallbackAccountScheduleRequestID()
	}

	var lastErr error
	for idx, acc := range accounts {
		if acc == nil {
			continue
		}
		accountName := accountFailoverDisplayName(acc)

		attemptStartedAt := time.Now()
		resp, upstreamCancel, _, err := h.forwardRequestToAccount(ctx, r, nil, acc, false, nil)
		releaseUpstream := func() {
			if upstreamCancel != nil {
				upstreamCancel()
				upstreamCancel = nil
			}
		}
		if err != nil {
			releaseUpstream()
			if e := codexIdentityError(err); e != nil {
				writeCodexModelsError(w, e.Status, e.Code, e.Message)
				return true
			}
			lastErr = err
			if !h.shouldFailOverAfterSoftFailure(ctx, acc.ID, err.Error(), accountSoftFailureCategoryServerError, 0) {
				break
			}
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonForwardError, 0, err.Error(), requestID, r.URL.Path)
			continue
		}
		if resp == nil {
			releaseUpstream()
			lastErr = fmt.Errorf("empty response from account %d", acc.ID)
			if !h.shouldFailOverAfterSoftFailure(ctx, acc.ID, lastErr.Error(), accountSoftFailureCategoryServerError, 0) {
				break
			}
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonEmptyResponse, 0, lastErr.Error(), requestID, r.URL.Path)
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			detail := readAndCloseResponseBody(resp, 1024)
			releaseUpstream()
			if detail == "" {
				detail = fmt.Sprintf("upstream returned %d", resp.StatusCode)
			}
			lastErr = fmt.Errorf("auth failed: %s", detail)
			_ = h.accountPoolService.MarkAccountAuthFailed(ctx, acc.ID, detail)
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonAuthFailed, resp.StatusCode, detail, requestID, r.URL.Path)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := h.accountRateLimitRetryAfter(resp)
			detail := readAndCloseResponseBody(resp, 1024)
			releaseUpstream()
			if detail == "" {
				detail = fmt.Sprintf("upstream returned %d", resp.StatusCode)
			}
			lastErr = fmt.Errorf("upstream retryable error: %s", detail)
			if usageLimit, ok := parseAccountUsageLimitWindow(detail, time.Now()); ok {
				_ = h.accountPoolService.MarkAccountUsageLimitExceeded(ctx, acc.ID, detail, usageLimit.planType, usageLimit.resetAt)
				h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonUsageLimit, resp.StatusCode, detail, requestID, r.URL.Path)
				continue
			}
			if !h.shouldFailOverAfterSoftFailure(ctx, acc.ID, detail, accountSoftFailureCategoryRateLimit, retryAfter) {
				break
			}
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonRateLimited, resp.StatusCode, detail, requestID, r.URL.Path)
			continue
		}

		if resp.StatusCode >= 500 {
			detail := readAndCloseResponseBody(resp, 1024)
			releaseUpstream()
			if detail == "" {
				detail = fmt.Sprintf("upstream returned %d", resp.StatusCode)
			}
			lastErr = fmt.Errorf("upstream retryable error: %s", detail)
			if !h.shouldFailOverAfterSoftFailure(ctx, acc.ID, detail, accountSoftFailureCategoryServerError, 0) {
				break
			}
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonServerError, resp.StatusCode, detail, requestID, r.URL.Path)
			continue
		}

		if resp.StatusCode >= 400 {
			if err := h.writeRawResponse(w, resp); err != nil {
				releaseUpstream()
				writeCodexModelsError(w, http.StatusBadGateway, "account_pool_response_error", "failed to read upstream model list from account pool")
				return true
			}
			releaseUpstream()
			return true
		}

		if err := h.writeRawResponse(w, resp); err != nil {
			releaseUpstream()
			writeCodexModelsError(w, http.StatusBadGateway, "account_pool_response_error", "failed to read upstream model list from account pool")
			return true
		}
		releaseUpstream()
		finalizeCtx, finalizeCancel := h.newAccountSuccessContext()
		_, _ = h.accountPoolService.MarkAccountSuccessIfNoNewerFailure(finalizeCtx, acc.ID, attemptStartedAt)
		if accountauth.IsChatGPTOAuthProvider(acc.ProviderType) {
			h.accountPoolService.TryEnqueueQuotaRefresh(acc.ID)
		}
		finalizeCancel()
		return true
	}

	reason := "all account pool candidates failed for Codex /v1/models"
	if lastErr != nil {
		reason = lastErr.Error()
	}
	writeCodexModelsError(w, http.StatusBadGateway, "account_pool_upstream_error", reason)
	return true
}

func writeCodexModelsError(w http.ResponseWriter, statusCode int, errType, message string) {
	writeAccountPipelineError(w, statusCode, errType, message)
}

func (h *Handler) tryWriteLocalCodexModels(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
	if !h.isCodexModelsRequest(r) || h.codexModelsProvider == nil {
		return false
	}

	responseBytes, handled, err := h.codexModelsProvider.GetCodexModelListResponse(ctx)
	if !handled {
		return false
	}

	if err != nil {
		writeCodexModelsError(w, http.StatusInternalServerError, "codex_models_error", "failed to load local Codex model catalog")
		return true
	}

	if len(responseBytes) == 0 {
		responseBytes = []byte(`{"object":"list","data":[]}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(responseBytes); err != nil {
		return true
	}

	return true
}

// shouldUseAccountPipeline 判断当前请求是否应走账号池链路
func (h *Handler) shouldUseAccountPipeline(path string) bool {
	return h.isAccountPipelinePath(path) &&
		h.config != nil &&
		h.config.AccountPool.Enabled &&
		h.accountPoolService != nil
}

func (h *Handler) isAccountPipelinePath(path string) bool {
	return isCodexAccountPoolRoute(path)
}

func (h *Handler) handleUnavailableAccountPipeline(w http.ResponseWriter, lifecycleManager *RequestLifecycleManager) {
	errType := "account_pool_disabled"
	message := "account pool is disabled for Codex /v1/responses, /v1/responses/compact, and /v1/alpha/search"
	failureKey := "account_pool_disabled"

	if h.config != nil && h.config.AccountPool.Enabled {
		errType = "account_pool_unavailable"
		failureKey = "account_pool_not_ready"
		message = "account pool service is not initialized for Codex /v1/responses, /v1/responses/compact, and /v1/alpha/search"
	}

	lifecycleManager.SetUpstream("account", "account-pool", "account-pool", 0)
	lifecycleManager.FailRequest(failureKey, message, http.StatusServiceUnavailable)
	writeAccountPipelineError(w, http.StatusServiceUnavailable, errType, message)
}

// detectSSERequest 统一SSE请求检测逻辑
func (h *Handler) detectSSERequest(r *http.Request, bodyBytes []byte) bool {
	// Codex standalone search 是同步 JSON 接口，且 input 会携带最近对话文本。
	// 必须先短路，避免对话内容中的 `"stream": true` 或 SSE 头被通用启发式检测误判为流式请求。
	if r != nil && r.URL.Path == codexStandaloneSearchPath {
		return false
	}
	if r != nil && r.URL != nil && isImageAPIPath(r.URL.Path) {
		return imageAPIRequestStreamEnabled(r, bodyBytes)
	}

	// 检查多种SSE请求模式:
	acceptHeader := r.Header.Get("Accept")
	cacheControlHeader := r.Header.Get("Cache-Control")
	streamHeader := r.Header.Get("stream")

	// 1. Accept头包含text/event-stream
	if strings.Contains(acceptHeader, "text/event-stream") {
		return true
	}

	// 2. stream头设置为true（显式流式标记）
	if strings.EqualFold(strings.TrimSpace(streamHeader), "true") {
		return true
	}

	// 3. 请求体包含stream参数为true（Anthropic/OpenAI常见格式）
	bodyStr := string(bodyBytes)
	if strings.Contains(bodyStr, `"stream":true`) || strings.Contains(bodyStr, `"stream": true`) {
		return true
	}
	if r.URL.Path == openAIImagesEditsPath && imageEditMultipartStreamEnabled(bodyBytes, r.Header.Get("Content-Type")) {
		return true
	}

	// 4. Cache-Control: no-cache 仅作为辅助信号，避免单独误判普通API请求
	if strings.Contains(cacheControlHeader, "no-cache") &&
		(strings.Contains(acceptHeader, "text/event-stream") || strings.EqualFold(strings.TrimSpace(streamHeader), "true")) {
		return true
	}

	return false
}

// UpdateConfig updates the handler configuration
func (h *Handler) UpdateConfig(cfg *config.Config) {
	h.config = cfg
}

// noOpFlusher 是一个不执行实际flush操作的flusher实现
// 用于测试和不支持Flusher的环境
type noOpFlusher struct{}

func (f *noOpFlusher) Flush() {
	// 不执行任何操作，避免panic但保持流式处理逻辑
}
