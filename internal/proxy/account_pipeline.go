package proxy

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/codexidentity"
	"cc-forwarder/internal/modelrewrite"
	"cc-forwarder/internal/privacy"
	"cc-forwarder/internal/proxy/handlers"
	svc "cc-forwarder/internal/service"
	"cc-forwarder/internal/store"
	"cc-forwarder/internal/tracking"
	"cc-forwarder/internal/transport"
)

const (
	defaultAccountConnectionFailureCooldown = 90 * time.Second
	defaultAccountProcessingFailureCooldown = 60 * time.Second
	defaultAccountSuccessWriteTimeout       = 2 * time.Second
	defaultAccountStreamTailDrainTimeout    = 1 * time.Second
	accountSoftFailureCategoryRateLimit     = "rate_limit"
	accountSoftFailureCategoryServerError   = "server_error"
	localNoAvailableProvidersMarker         = "no_available_providers::ccf_local"

	chatGPTCodexResponsesURL  = "https://chatgpt.com/backend-api/codex/responses"
	chatGPTCodexCompactURL    = "https://chatgpt.com/backend-api/codex/responses/compact"
	chatGPTCodexModelsURL     = "https://chatgpt.com/backend-api/codex/models"
	chatGPTCodexSearchURL     = "https://chatgpt.com/backend-api/codex/alpha/search"
	codexStandaloneSearchPath = svc.CodexStandaloneSearchPath
	openAIBetaResponsesValue  = "responses=experimental"
	defaultOAuthOriginator    = "codex_cli_rs"
)

type accountUsageLimitErrorEnvelope struct {
	Error accountUsageLimitErrorDetail `json:"error"`
}

type accountUsageLimitErrorDetail struct {
	Type            string `json:"type"`
	Message         string `json:"message"`
	PlanType        string `json:"plan_type"`
	ResetsAt        int64  `json:"resets_at"`
	ResetsInSeconds int64  `json:"resets_in_seconds"`
}

type accountUsageLimitWindow struct {
	planType string
	resetAt  time.Time
}

type accountStreamStatusError struct {
	status     string
	modelName  string
	usageLimit *accountUsageLimitWindow
	cause      error
}

func (e *accountStreamStatusError) Error() string {
	if e == nil {
		return ""
	}
	status := strings.TrimSpace(e.status)
	if status == "" {
		status = "error"
	}
	modelName := strings.TrimSpace(e.modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	if e.cause == nil {
		return fmt.Sprintf("stream_status:%s:model:%s", status, modelName)
	}
	return fmt.Sprintf("stream_status:%s:model:%s: %v", status, modelName, e.cause)
}

func (e *accountStreamStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// handleAccountPipeline 账号池链路（阶段2）
func (h *Handler) handleAccountPipeline(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, lifecycleManager *RequestLifecycleManager) {
	if h.accountPoolService == nil {
		lifecycleManager.SetUpstream("account", "account-pool", "account-pool", 0)
		lifecycleManager.FailRequest("account_pool_not_ready", "account pool service is not initialized", http.StatusServiceUnavailable)
		writeAccountPipelineError(w, http.StatusServiceUnavailable, "account_pool_unavailable", "account pool service is not initialized")
		return
	}

	var identityErr error
	ctx, identityErr = h.attachCodexIdentityRequest(ctx, r)
	if identityErr != nil {
		e := codexIdentityError(identityErr)
		lifecycleManager.FailRequest(e.Code, e.Message, e.Status)
		writeAccountPipelineError(w, e.Status, e.Code, e.Message)
		return
	}
	requestID := ""
	if lifecycleManager != nil {
		requestID = lifecycleManager.GetRequestID()
	}
	if strings.TrimSpace(requestID) == "" {
		requestID = newFallbackAccountScheduleRequestID()
	}
	var schedule svc.AccountScheduleResult
	var codexScope svc.CodexModelRouteScope
	var cacheGenerations map[int64]uint64
	var modelRoutingFailureReason string
	var modelRoutingFailureMessage string
	var err error
	modelRouted := h.codexModelRouter != nil && h.codexModelRouter.Enabled() && (r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/responses/compact")
	if modelRouted {
		model := h.extractModelFromRequestBody(bodyBytes, r.URL.Path)
		var routeResult svc.CodexModelRouteResult
		routeResult, err = h.codexModelRouter.Prepare(ctx, model, r.URL.Path)
		codexScope = routeResult.Scope
		cacheGenerations = routeResult.CacheGenerations
		schedule.Accounts = routeResult.Accounts
		now := time.Now()
		schedule.Snapshot = &svc.LatestAccountScheduleSnapshot{
			RequestID: requestID, CapturedAt: now, UpdatedAt: now, RequestPath: r.URL.Path,
			RequestModel: model, ModelRoutingFamily: "codex", ModelRoutingVersion: codexScope.SnapshotVersion,
			RouteScopeKind: codexScope.Kind, RouteScopeGroupID: codexScope.GroupID, RouteScopeGroupName: codexScope.GroupName,
			RouteMode: codexScope.Mode, RouteTargetAccountID: codexScope.TargetAccountID,
			Candidates: routeResult.Candidates,
		}
		if len(routeResult.Accounts) > 0 {
			schedule.Snapshot.SelectedAccountID = routeResult.Accounts[0].ID
			schedule.Snapshot.SelectedAccountName = accountFailoverDisplayName(routeResult.Accounts[0])
		}
		if recorder, ok := h.accountPoolService.(interface {
			SaveLatestScheduleSnapshot(*svc.LatestAccountScheduleSnapshot)
		}); ok {
			recorder.SaveLatestScheduleSnapshot(schedule.Snapshot)
		}
		if err == nil && routeResult.Scope.Kind == "unassigned" {
			modelRoutingFailureReason = routeResult.Scope.UnassignedReason
			if modelRoutingFailureReason == "" {
				modelRoutingFailureReason = svc.CodexModelRoutingUnassigned
			}
			modelRoutingFailureMessage = "Codex 模型未配置账号组"
			if modelRoutingFailureReason == svc.CodexModelRoutingMissingModel {
				modelRoutingFailureMessage = "Codex 请求未携带 model"
			} else if modelRoutingFailureReason == svc.CodexModelRoutingLoadFailed {
				modelRoutingFailureMessage = "Codex 模型路由配置加载失败"
			}
		}
	} else {
		schedule, err = h.accountPoolService.PrepareSchedulableAccounts(ctx, requestID, r.URL.Path)
	}
	// B3：调度草稿持久化（白名单 DTO）。decisionAt 同笔补写 route_decision_at。
	if snap := schedule.Snapshot; snap != nil && h.usageTracker != nil && requestID != "" {
		if payload := marshalScheduleSnapshot(snap); payload != "" {
			decisionAt := snap.CapturedAt
			h.usageTracker.RecordRequestUpdate(requestID, tracking.UpdateOptions{
				ScheduleSnapshotJSON: &payload,
				RouteDecisionAt:      &decisionAt,
			})
		}
	}
	if modelRoutingFailureReason != "" {
		_ = h.completeAccountScheduleSnapshot(ctx, requestID, 0, "", modelRoutingFailureReason, modelRoutingFailureMessage)
		lifecycleManager.SetUpstream("account", "model-routing", "", 0)
		lifecycleManager.FailRequest(modelRoutingFailureReason, modelRoutingFailureMessage, http.StatusUnprocessableEntity)
		writeAccountPipelineError(w, http.StatusUnprocessableEntity, modelRoutingFailureReason, modelRoutingFailureMessage)
		return
	}
	if err != nil {
		if modelRouted {
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, 0, "", svc.AccountScheduleOutcomeTransientFailure, "failed to load model routing accounts")
			lifecycleManager.SetUpstream("account", "model-routing", "", 0)
		} else {
			lifecycleManager.SetUpstream("account", "account-pool", "account-pool", 0)
		}
		lifecycleManager.FailRequest("account_pool_load_failed", err.Error(), http.StatusServiceUnavailable)
		writeAccountPipelineError(w, http.StatusServiceUnavailable, "account_pool_unavailable", "failed to load schedulable accounts")
		return
	}
	accounts := schedule.Accounts
	if len(accounts) == 0 {
		failureReason := "account_pool_empty"
		errorType := "account_pool_unavailable"
		message := "no schedulable account"
		if modelRouted {
			failureReason = svc.CodexModelRoutingNoAvailable
			errorType = svc.CodexModelRoutingNoAvailable
			message = "模型路由账号组中没有可用账号"
			if codexScope.Mode == "manual_fixed" {
				failureReason = svc.CodexModelRoutingFixedUnavailable
				errorType = svc.CodexModelRoutingFixedUnavailable
				message = "模型路由固定账号不可用"
			}
		}
		if r.URL.Path == codexStandaloneSearchPath {
			failureReason = "codex_search_oauth_unavailable"
			errorType = "codex_search_oauth_unavailable"
			message = "Codex /v1/alpha/search requires an enabled and available ChatGPT OAuth account"
		}
		outcome := svc.AccountScheduleOutcomeNoSchedulableAccounts
		if modelRouted {
			outcome = errorType
			h.codexModelRouter.Settle(codexScope, 0, outcome)
		}
		_ = h.completeAccountScheduleSnapshot(ctx, requestID, 0, "", outcome, message)
		if modelRouted {
			lifecycleManager.SetUpstream("account", "model-routing", "model-routing", 0)
		} else {
			lifecycleManager.SetUpstream("account", "account-pool", "account-pool", 0)
		}
		lifecycleManager.FailRequest(failureReason, message, http.StatusServiceUnavailable)
		writeAccountPipelineError(w, http.StatusServiceUnavailable, errorType, message)
		return
	}

	isSSE := h.detectSSERequest(r, bodyBytes)
	var lastErr error
	attemptedAccount := false
	lastCodexAccountID := int64(0)
	lastCodexOutcome := ""

	for idx, acc := range accounts {
		if acc == nil {
			continue
		}
		attemptedAccount = true
		if modelRouted {
			lastCodexAccountID = acc.ID
		}
		accountName := accountFailoverDisplayName(acc)

		upstreamSource := "account-pool"
		if modelRouted {
			upstreamSource = "model-routing"
		}
		lifecycleManager.SetUpstream("account", upstreamSource, accountName, acc.ID)
		lifecycleManager.SetEndpoint(accountName)
		lifecycleManager.UpdateStatus("forwarding", idx, 0)
		attemptStartedAt := time.Now()
		hasNextAccount := hasNextAccountFailoverCandidate(accounts, idx)

		resp, upstreamCancel, traceState, forwardErr := h.forwardRequestToAccount(ctx, r, bodyBytes, acc, isSSE, lifecycleManager)
		// upstreamCancel 同时可能被 releaseUpstream 和 tail-drain 超时回调持有；context.CancelFunc 幂等，重复调用是安全的。
		releaseUpstream := func() {
			if upstreamCancel != nil {
				upstreamCancel()
				upstreamCancel = nil
			}
		}
		if forwardErr != nil {
			releaseUpstream()
			if e := codexIdentityError(forwardErr); e != nil {
				if modelRouted {
					h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomeInvalidRequest)
				}
				_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeInvalidRequest, e.Code)
				lifecycleManager.FailRequest(e.Code, e.Message, e.Status)
				writeAccountPipelineError(w, e.Status, e.Code, e.Message)
				return
			}
			var bodyErr *requestBodyError
			if errors.As(forwardErr, &bodyErr) {
				if modelRouted {
					h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomeInvalidRequest)
				}
				_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeInvalidRequest, bodyErr.Code)
				slog.Warn(fmt.Sprintf("⚠️ [账号请求] [%s] 本地报文检查未通过", requestID),
					"account_id", acc.ID, "path", r.URL.Path, "code", bodyErr.Code)
				lifecycleManager.FailRequest(bodyErr.Code, bodyErr.Message, bodyErr.StatusCode)
				writeAccountPipelineError(w, bodyErr.StatusCode, bodyErr.Code, bodyErr.Message)
				return
			}
			// 🛡️ 隐私策略短路：本地策略拒绝不是上游失败，
			// 不冷却账号、不记软失败、不继续换号
			if policyErr := handlers.AsPrivacyPolicyError(forwardErr); policyErr != nil {
				if modelRouted {
					h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomePrivacyBlocked)
				}
				_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomePrivacyBlocked, policyErr.Code)
				lifecycleManager.FailRequest(handlers.PrivacyFailureReason(policyErr), policyErr.Message, policyErr.StatusCode)
				writeAccountPipelineError(w, policyErr.StatusCode, policyErr.Code, policyErr.Message)
				return
			}
			lastErr = forwardErr
			shouldFailover := h.shouldFailOverAfterSoftFailure(ctx, acc.ID, forwardErr.Error(), accountSoftFailureCategoryServerError, 0)
			// WroteHeaders 前失败 = 请求确定未发出，重放安全；非 pinned 账号立即换
			// 同层下一候选（软失败计数已照记）。pinned 账号维持穿透语义，保住
			// 「固定直到严格不可用」：达阈值冷却后由后续请求降级，pin 保留。
			if !traceState.WroteHeaders() && !schedule.Pinned {
				logAccountPipelineAttemptFailure(requestID, acc, 0, FailoverReasonConnectionFailedBeforeHeaders, forwardErr.Error(), hasNextAccount)
				if modelRouted {
					lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
				}
				_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, forwardErr.Error())
				h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonConnectionFailedBeforeHeaders, 0, forwardErr.Error(), requestID, r.URL.Path)
				continue
			}
			logAccountPipelineAttemptFailure(requestID, acc, 0, FailoverReasonForwardError, forwardErr.Error(), shouldFailover && hasNextAccount)
			if modelRouted {
				lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
			}
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, forwardErr.Error())
			if !shouldFailover {
				break
			}
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonForwardError, 0, forwardErr.Error(), requestID, r.URL.Path)
			continue
		}

		if resp == nil {
			lastErr = fmt.Errorf("empty response from account %d", acc.ID)
			releaseUpstream()
			shouldFailover := h.shouldFailOverAfterSoftFailure(ctx, acc.ID, lastErr.Error(), accountSoftFailureCategoryServerError, 0)
			logAccountPipelineAttemptFailure(requestID, acc, 0, FailoverReasonEmptyResponse, lastErr.Error(), shouldFailover && hasNextAccount)
			if modelRouted {
				lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
			}
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, lastErr.Error())
			if !shouldFailover {
				break
			}
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonEmptyResponse, 0, lastErr.Error(), requestID, r.URL.Path)
			continue
		}

		if modelRouted && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			body, readErr := io.ReadAll(resp.Body)
			if readErr == nil {
				_ = resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(body))
				if handlers.IsModelUnsupportedError(string(body)) {
					_ = resp.Body.Close()
					releaseUpstream()
					h.codexModelRouter.RecordNegativeHit(acc.ID, r.URL.Path, codexScope.Model, cacheGenerations[acc.ID])
					lastErr = fmt.Errorf("model unsupported by account %d", acc.ID)
					lastCodexOutcome = "model_unsupported"
					_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, "model_unsupported", "模型不受支持")
					continue
				}
			}
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			detail := readAndCloseResponseBody(resp, 1024)
			releaseUpstream()
			if detail == "" {
				detail = fmt.Sprintf("upstream returned %d", resp.StatusCode)
			}
			lastErr = fmt.Errorf("auth failed: %s", detail)
			if modelRouted {
				lastCodexOutcome = svc.AccountScheduleOutcomeAuthFailed
			}
			_ = h.accountPoolService.MarkAccountAuthFailed(ctx, acc.ID, detail)
			logAccountPipelineAttemptFailure(requestID, acc, resp.StatusCode, FailoverReasonAuthFailed, detail, hasNextAccount)
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeAuthFailed, detail)
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonAuthFailed, resp.StatusCode, detail, requestID, r.URL.Path)
			continue
		}

		if matched, detail := h.isLocalNoAvailableProviders503Response(resp); matched {
			if detail == "" {
				detail = h.accountLocalNoAvailableProvidersMarker()
			}
			if writeErr := h.writeRawResponse(w, resp); writeErr != nil {
				releaseUpstream()
				if modelRouted {
					h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomeTransientFailure)
				}
				lifecycleManager.FailRequest("account_pipeline_response_write_error", writeErr.Error(), resp.StatusCode)
				_ = h.failAndFinalizeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, writeErr.Error())
				return
			}
			releaseUpstream()
			if modelRouted {
				h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomePassthroughNoAvailableProvider)
			}
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomePassthroughNoAvailableProvider, detail)
			lifecycleManager.FailRequest("upstream_no_available_providers", detail, resp.StatusCode)
			return
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
				if modelRouted {
					lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
				}
				_ = h.accountPoolService.MarkAccountUsageLimitExceeded(ctx, acc.ID, detail, usageLimit.planType, usageLimit.resetAt)
				logAccountPipelineAttemptFailure(requestID, acc, resp.StatusCode, FailoverReasonUsageLimit, detail, hasNextAccount)
				_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, detail)
				h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonUsageLimit, resp.StatusCode, detail, requestID, r.URL.Path)
				continue
			}
			shouldFailover := h.shouldFailOverAfterSoftFailure(ctx, acc.ID, detail, accountSoftFailureCategoryRateLimit, retryAfter)
			if modelRouted {
				lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
			}
			logAccountPipelineAttemptFailure(requestID, acc, resp.StatusCode, FailoverReasonRateLimited, detail, shouldFailover && hasNextAccount)
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, detail)
			if !shouldFailover {
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
			shouldFailover := h.shouldFailOverAfterSoftFailure(ctx, acc.ID, detail, accountSoftFailureCategoryServerError, 0)
			if modelRouted {
				lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
			}
			logAccountPipelineAttemptFailure(requestID, acc, resp.StatusCode, FailoverReasonServerError, detail, shouldFailover && hasNextAccount)
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, detail)
			if !shouldFailover {
				break
			}
			h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonServerError, resp.StatusCode, detail, requestID, r.URL.Path)
			continue
		}

		// 其余 4xx 视为客户端请求问题，直接透传，不进行账号切换
		if resp.StatusCode >= 400 {
			detail := fmt.Sprintf("upstream returned %d", resp.StatusCode)
			logAccountPipelineAttemptFailure(requestID, acc, resp.StatusCode, "client_error_passthrough", detail, false)
			if writeErr := h.writeRawResponse(w, resp); writeErr != nil {
				releaseUpstream()
				if modelRouted {
					h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomeTransientFailure)
				}
				lifecycleManager.FailRequest("account_pipeline_response_write_error", writeErr.Error(), resp.StatusCode)
				_ = h.accountPoolService.MarkAccountTransientFailure(ctx, acc.ID, writeErr.Error(), h.accountProcessingFailureCooldown())
				_ = h.failAndFinalizeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, writeErr.Error())
				return
			}
			releaseUpstream()
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomePassthroughOther4xx, detail)
			lifecycleManager.FailRequest("upstream_client_error", detail, resp.StatusCode)
			if modelRouted {
				h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomePassthroughOther4xx)
			}
			return
		}

		lifecycleManager.UpdateStatus("processing", idx, resp.StatusCode)
		var processErr error
		if isSSE {
			processErr = h.processAccountStreamingResponse(ctx, w, resp, lifecycleManager, accountName, upstreamCancel)
		} else {
			processErr = h.processAccountRegularResponse(w, resp, lifecycleManager, accountName, r)
		}
		releaseUpstream()
		if processErr != nil {
			if isCancelledAccountStreamError(processErr) {
				if modelRouted {
					h.codexModelRouter.Settle(codexScope, acc.ID, "cancelled")
				}
				return
			}
			lastErr = processErr
			if isSSE {
				status, _ := parseAccountStreamStatusError(processErr)
				switch status {
				case "auth_error":
					_ = h.accountPoolService.MarkAccountAuthFailed(ctx, acc.ID, processErr.Error())
				case "rate_limited":
					if usageLimit, ok := parseAccountStreamUsageLimitWindow(processErr); ok {
						_ = h.accountPoolService.MarkAccountUsageLimitExceeded(ctx, acc.ID, processErr.Error(), usageLimit.planType, usageLimit.resetAt)
					} else {
						_ = h.shouldFailOverAfterSoftFailure(ctx, acc.ID, processErr.Error(), accountSoftFailureCategoryRateLimit, 0)
					}
				case "stream_error", "error", "timeout", "network_error":
					_ = h.shouldFailOverAfterSoftFailure(ctx, acc.ID, processErr.Error(), accountSoftFailureCategoryServerError, 0)
				default:
					_ = h.accountPoolService.MarkAccountTransientFailure(ctx, acc.ID, processErr.Error(), h.accountProcessingFailureCooldown())
				}
			} else {
				shouldFailover := h.shouldFailOverAfterSoftFailure(ctx, acc.ID, processErr.Error(), accountSoftFailureCategoryServerError, 0)
				if modelRouted {
					lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
				}
				_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, processErr.Error())
				if !shouldFailover {
					break
				}
				h.notifyAccountFailover(ctx, accounts, idx, accountName, FailoverReasonProcessingError, 0, processErr.Error(), requestID, r.URL.Path)
				continue
			}
			// 流式响应一旦开始输出，无法切换到下一个账号，直接终止
			if isSSE {
				codexOutcome := accountScheduleOutcomeForCodexStreamError(processErr)
				if modelRouted {
					h.codexModelRouter.Settle(codexScope, acc.ID, codexOutcome)
				}
				if modelRouted {
					lastCodexOutcome = codexOutcome
				}
				_ = h.failAndFinalizeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, processErr.Error())
				return
			}
			if modelRouted {
				lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
			}
			_ = h.completeAccountScheduleSnapshot(ctx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeTransientFailure, processErr.Error())
			continue
		}

		finalizeCtx, finalizeCancel := h.newAccountSuccessContext()
		_, _ = h.accountPoolService.MarkAccountSuccessIfNoNewerFailure(finalizeCtx, acc.ID, attemptStartedAt)
		if accountauth.IsChatGPTOAuthProvider(acc.ProviderType) {
			h.accountPoolService.TryEnqueueQuotaRefresh(acc.ID)
		}
		_ = h.completeAccountScheduleSnapshot(finalizeCtx, requestID, acc.ID, accountName, svc.AccountScheduleOutcomeSuccess, "")
		if modelRouted {
			h.codexModelRouter.Settle(codexScope, acc.ID, svc.AccountScheduleOutcomeSuccess)
		}
		finalizeCancel()
		return
	}

	reason := "all accounts failed"
	if lastErr != nil {
		reason = lastErr.Error()
	}
	if modelRouted {
		if lastCodexOutcome == "" {
			lastCodexOutcome = svc.AccountScheduleOutcomeTransientFailure
		}
		h.codexModelRouter.Settle(codexScope, lastCodexAccountID, lastCodexOutcome)
	}
	_ = h.completeAccountScheduleSnapshot(ctx, requestID, 0, "", svc.AccountScheduleOutcomeTransientFailure, reason)
	if !attemptedAccount {
		lifecycleManager.SetUpstream("account", "account-pool", "account-pool", 0)
	}
	lifecycleManager.FailRequest("account_pool_exhausted", reason, http.StatusServiceUnavailable)
	writeAccountPipelineError(w, http.StatusServiceUnavailable, "account_pool_unavailable", reason)
}

func (h *Handler) shouldFailOverAfterSoftFailure(ctx context.Context, accountID int64, reason, category string, retryAfter time.Duration) bool {
	if h == nil || h.accountPoolService == nil {
		return true
	}

	shouldFailover, err := h.accountPoolService.RecordAccountSoftFailure(ctx, accountID, reason, category, retryAfter)
	if err != nil {
		return true
	}
	return shouldFailover
}

func logAccountPipelineAttemptFailure(requestID string, acc *store.UpstreamAccountRecord, statusCode int, reasonType, detail string, willTryNext bool) {
	accountID := int64(0)
	accountName := ""
	providerType := ""
	if acc != nil {
		accountID = acc.ID
		accountName = acc.AccountName
		providerType = acc.ProviderType
	}
	action := "stop"
	if willTryNext {
		action = "try_next_account"
	}
	message := fmt.Sprintf("⚠️ [账号池上游失败] [%s]", requestID)
	if statusCode > 0 {
		slog.Warn(message,
			"account_id", accountID,
			"account_name", accountName,
			"provider_type", providerType,
			"status_code", statusCode,
			"reason_type", reasonType,
			"action", action,
			"reason", detail,
		)
		return
	}
	slog.Warn(message,
		"account_id", accountID,
		"account_name", accountName,
		"provider_type", providerType,
		"reason_type", reasonType,
		"action", action,
		"reason", detail,
	)
}

func (h *Handler) completeAccountScheduleSnapshot(ctx context.Context, requestID string, accountID int64, accountName, outcome, finalError string) error {
	snapshot, err := h.updateAccountScheduleSnapshot(ctx, requestID, accountID, accountName, outcome, finalError)
	if err != nil {
		return err
	}
	h.persistAccountScheduleSnapshot(requestID, snapshot)
	return nil
}

func (h *Handler) updateAccountScheduleSnapshot(ctx context.Context, requestID string, accountID int64, accountName, outcome, finalError string) (*svc.LatestAccountScheduleSnapshot, error) {
	if h.accountPoolService == nil {
		return nil, nil
	}
	return h.accountPoolService.CompleteLatestScheduleSnapshot(ctx, requestID, accountID, accountName, outcome, finalError)
}

func (h *Handler) persistAccountScheduleSnapshot(requestID string, snapshot *svc.LatestAccountScheduleSnapshot) {
	// B3：complete 覆盖写。matched=false（他人快照/超 TTL）时返回 nil，绝不写入错误快照。
	if snapshot != nil && h.usageTracker != nil && requestID != "" {
		if payload := marshalScheduleSnapshot(snapshot); payload != "" {
			h.usageTracker.RecordRequestUpdate(requestID, tracking.UpdateOptions{
				ScheduleSnapshotJSON: &payload,
			})
		}
	}
}

func (h *Handler) failAndFinalizeAccountScheduleSnapshot(ctx context.Context, requestID string, accountID int64, accountName, finalError string) error {
	// 先记录当前候选的运行态结果，再用 accountID=0 收口请求终态；只持久化收口后的最终 JSON。
	_, attemptErr := h.updateAccountScheduleSnapshot(ctx, requestID, accountID, accountName, svc.AccountScheduleOutcomeTransientFailure, finalError)
	finalSnapshot, finalErr := h.updateAccountScheduleSnapshot(ctx, requestID, 0, "", svc.AccountScheduleOutcomeTransientFailure, finalError)
	if finalErr == nil {
		h.persistAccountScheduleSnapshot(requestID, finalSnapshot)
	}
	return errors.Join(attemptErr, finalErr)
}

func newFallbackAccountScheduleRequestID() string {
	var suffix [4]byte
	if _, err := crand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("account-schedule-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("account-schedule-%d-%s", time.Now().UnixNano(), hex.EncodeToString(suffix[:]))
}

func (h *Handler) newAccountStreamForwardContext(parent context.Context) (context.Context, context.CancelFunc) {
	requestCtx, cancel := context.WithCancel(context.Background())
	tailDrain := h.accountStreamTailDrainTimeout()
	if tailDrain <= 0 {
		tailDrain = time.Second
	}

	done := make(chan struct{})
	if parent != nil {
		go func() {
			select {
			case <-parent.Done():
				timer := time.NewTimer(tailDrain)
				defer timer.Stop()
				select {
				case <-timer.C:
					cancel()
				case <-done:
				}
			case <-done:
			}
		}()
	}

	var once sync.Once
	return requestCtx, func() {
		once.Do(func() {
			close(done)
			cancel()
		})
	}
}

func (h *Handler) newAccountSuccessContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultAccountSuccessWriteTimeout)
}

func (h *Handler) accountStreamTailDrainTimeout() time.Duration {
	return defaultAccountStreamTailDrainTimeout
}

func (h *Handler) forwardRequestToAccount(ctx context.Context, r *http.Request, bodyBytes []byte, acc *store.UpstreamAccountRecord, isSSE bool, lifecycleManager *RequestLifecycleManager) (*http.Response, context.CancelFunc, *handlers.UpstreamTraceState, error) {
	var snapshotErr error
	ctx, snapshotErr = h.attachCodexIdentityRequest(ctx, r)
	if snapshotErr != nil {
		return nil, nil, nil, snapshotErr
	}
	requestID := ""
	if lifecycleManager != nil {
		requestID = lifecycleManager.GetRequestID()
	}
	if acc != nil && acc.StripCodexInternalMetadata && r.Method == http.MethodPost && isCodexResponsesAPIPath(r.URL.Path) {
		filtered, removed, err := stripCodexInternalMetadata(bodyBytes)
		if err != nil {
			return nil, nil, nil, &requestBodyError{
				StatusCode: http.StatusBadRequest, Code: requestBodyCodeCodexMetadataInvalid,
				Message: "Codex 内部元数据过滤失败：请求 JSON 或 input 结构无效", Cause: err,
			}
		}
		bodyBytes = filtered
		slog.Info(fmt.Sprintf("🧹 [Codex 内部元数据] [%s] 出站兼容处理完成", requestID),
			"account_id", acc.ID, "path", r.URL.Path,
			"enabled", true, "removed_items", removed)
	}
	targetURL, err := resolveAccountTargetURL(acc, r.URL.Path, r.URL.RawQuery)
	if err != nil {
		return nil, nil, nil, err
	}
	var upstreamModel string
	bodyBytes, upstreamModel = prepareCodexAccountBodyForUpstream(bodyBytes, acc, r.URL.Path)
	if lifecycleManager != nil {
		lifecycleManager.SetUpstreamModel(upstreamModel)
	}

	bodyBytes, authHeaders, prepared, err := h.prepareAccountIdentity(ctx, r, bodyBytes, acc, targetURL)
	if err != nil {
		return nil, nil, nil, err
	}
	wireBody, sendZstd, compressionMode, err := prepareAccountOutboundBody(r, bodyBytes, acc)
	if err != nil {
		return nil, nil, nil, err
	}
	if compressionMode != "disabled" {
		slog.Info(fmt.Sprintf("🗜️ [账号请求压缩] [%s] 出站请求已规范化", requestID),
			"account", acc.AccountName,
			"path", r.URL.Path,
			"mode", compressionMode,
			"plaintext_bytes", len(bodyBytes),
			"wire_bytes", len(wireBody),
		)
	}

	requestCtx := ctx
	var release context.CancelFunc
	if isSSE {
		requestCtx, release = h.newAccountStreamForwardContext(ctx)
	}
	var onWroteRequest func(time.Time)
	if lifecycleManager != nil {
		onWroteRequest = lifecycleManager.SetFirstTokenStartTime
	}
	requestCtx, traceState := handlers.WithUpstreamTrace(requestCtx, onWroteRequest)

	req, err := http.NewRequestWithContext(requestCtx, r.Method, targetURL, bytes.NewReader(wireBody))
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, traceState, fmt.Errorf("failed to create request: %w", err)
	}

	copyRequestHeadersForAccount(r, req)
	if sendZstd {
		req.Header.Set("Content-Encoding", requestContentEncodingZstd)
	} else {
		req.Header.Del("Content-Encoding")
	}
	req.Header.Del("Content-Length")
	req.ContentLength = int64(len(wireBody))
	if prepared.IdentityEnabled {
		prepared.ApplyHeaders(req.Header, req.URL)
	} else if accountauth.IsChatGPTOAuthProvider(acc.ProviderType) {
		switch r.URL.Path {
		case "/v1/models":
			applyOpenAIChatGPTModelsHeaders(req)
		case codexStandaloneSearchPath:
			applyOpenAIChatGPTSearchHeaders(req)
		default:
			applyOpenAIChatGPTOAuthHeaders(req)
		}
	}
	restoreAutomaticThirdPartyState(r, req, prepared)
	for key, values := range authHeaders {
		req.Header[key] = append([]string(nil), values...)
	}

	client, err := h.getAccountHTTPClient(isSSE)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, traceState, err
	}
	if prepared.IdentityEnabled {
		client = identityBoundClient(client)
	}
	resp, err := client.Do(req)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, traceState, fmt.Errorf("request failed: %w", err)
	}
	h.observeAccountIdentityResponse(resp, prepared)
	if sendZstd && resp.StatusCode == http.StatusUnsupportedMediaType {
		slog.Warn(fmt.Sprintf("⚠️ [账号请求压缩] [%s] 上游拒绝 zstd，请求不会静默降级为明文", requestID),
			"account", acc.AccountName,
			"path", r.URL.Path,
			"status_code", resp.StatusCode,
		)
	}
	return resp, release, traceState, nil
}

func prepareAccountOutboundBody(r *http.Request, body []byte, acc *store.UpstreamAccountRecord) ([]byte, bool, string, error) {
	if r == nil || !isCodexResponsesAPIPath(r.URL.Path) || acc == nil || len(body) == 0 || !acc.EnableRequestCompression || !store.SupportsAccountRequestCompression(acc.ProviderType) {
		return body, false, "disabled", nil
	}
	metadata := normalizedRequestBodyMetadataFromRequest(r)
	if metadata.canReuseOriginalCompressed(body) {
		return metadata.originalCompressedBody, true, "reuse_original", nil
	}
	compressed, err := compressNormalizedRequestBody(metadata, body)
	if err != nil {
		return nil, false, "", fmt.Errorf("failed to compress outbound request body: %w", err)
	}
	return compressed, true, "recompress", nil
}

// applyAccountPrivacyFilter 账号池链路出站隐私过滤。
// 规则可按 upstream_type=account、账号 ID、provider type、path 生效。
func (h *Handler) applyAccountPrivacyFilter(r *http.Request, bodyBytes []byte, acc *store.UpstreamAccountRecord) ([]byte, error) {
	if h == nil || h.privacyFilter == nil || acc == nil || r == nil {
		return bodyBytes, nil
	}
	req := privacy.Request{
		Path:         r.URL.Path,
		Method:       r.Method,
		UpstreamType: privacy.UpstreamTypeAccount,
		AccountID:    acc.ID,
		ProviderType: acc.ProviderType,
		ContentType:  r.Header.Get("Content-Type"),
	}
	return handlers.ApplyPrivacyFilter(h.privacyFilter, r, req, bodyBytes)
}

// prepareCodexAccountBodyForUpstream 应用账号级模型改写；第二个返回值是改写后的上游模型，未改写为空串。
func prepareCodexAccountBodyForUpstream(bodyBytes []byte, acc *store.UpstreamAccountRecord, path string) ([]byte, string) {
	if !shouldRewriteCodexAccountModel(acc, path) || len(bytes.TrimSpace(bodyBytes)) == 0 {
		return bodyBytes, ""
	}

	fields, err := codexidentity.JSONFields(bodyBytes)
	if err != nil {
		return bodyBytes, ""
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil {
		return bodyBytes, ""
	}
	targetModel, shouldRewrite := rewriteCodexAccountModel(acc, path, model)
	if !shouldRewrite {
		return bodyBytes, ""
	}
	rewritten, err := codexidentity.EditObject(bodyBytes, map[string][]byte{"model": codexidentity.JSONString(targetModel)})
	if err != nil {
		return bodyBytes, ""
	}

	return rewritten, targetModel
}

func shouldRewriteCodexAccountModel(acc *store.UpstreamAccountRecord, path string) bool {
	if acc == nil || accountauth.IsChatGPTOAuthProvider(acc.ProviderType) {
		return false
	}
	return isCodexResponsesAPIPath(path) && strings.TrimSpace(acc.ModelRewriteRules) != ""
}

// isCodexResponsesAPIPath 表示使用 Responses 请求语义的路径。
// 只有这些路径允许账号级模型改写与 zstd 压缩；standalone search 虽走账号池，但不属于此范围。
func isCodexResponsesAPIPath(path string) bool {
	return path == "/v1/responses" || path == "/v1/responses/compact"
}

// isCodexAccountPoolRoute 表示由 Codex 账号池独占处理、禁止回退到普通 endpoint 的全部路径。
func isCodexAccountPoolRoute(path string) bool {
	return isCodexResponsesAPIPath(path) || path == codexStandaloneSearchPath
}

func rewriteCodexAccountModel(acc *store.UpstreamAccountRecord, path, model string) (string, bool) {
	if acc == nil {
		return "", false
	}
	target, rewritten, err := modelrewrite.Rewrite(acc.ModelRewriteRules, path, model)
	if err != nil {
		slog.Warn("⚠️ [账号模型改写] 忽略无效规则", "account", acc.AccountName, "error", err)
		return "", false
	}
	return target, rewritten
}

func (h *Handler) isLocalNoAvailableProviders503Response(resp *http.Response) (bool, string) {
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		return false, ""
	}
	bodyText := readAndRestoreResponseBody(resp, 1024)
	lower := strings.ToLower(strings.TrimSpace(bodyText))
	if lower == "" {
		return false, bodyText
	}
	return strings.Contains(lower, strings.ToLower(h.accountLocalNoAvailableProvidersMarker())), bodyText
}

func (h *Handler) accountLocalNoAvailableProvidersMarker() string {
	if h != nil && h.config != nil {
		marker := strings.TrimSpace(h.config.AccountPool.FailurePolicy.LocalNoAvailableProvidersMarker)
		if marker != "" {
			return marker
		}
	}
	return localNoAvailableProvidersMarker
}

func (h *Handler) accountConnectionFailureCooldown() time.Duration {
	if h != nil && h.config != nil && h.config.AccountPool.FailurePolicy.Cooldowns.ConnectionFailure > 0 {
		return h.config.AccountPool.FailurePolicy.Cooldowns.ConnectionFailure
	}
	return defaultAccountConnectionFailureCooldown
}

func (h *Handler) accountProcessingFailureCooldown() time.Duration {
	if h != nil && h.config != nil && h.config.AccountPool.FailurePolicy.Cooldowns.ProcessingFailure > 0 {
		return h.config.AccountPool.FailurePolicy.Cooldowns.ProcessingFailure
	}
	return defaultAccountProcessingFailureCooldown
}

func (h *Handler) accountRateLimitRetryAfter(resp *http.Response) time.Duration {
	if h == nil || h.config == nil || h.config.AccountPool.FailurePolicy.RespectRetryAfter == nil || !*h.config.AccountPool.FailurePolicy.RespectRetryAfter {
		return 0
	}
	return parseAccountRetryAfter(resp)
}

func parseAccountRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	at, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}
	if d := time.Until(at); d > 0 {
		return d
	}
	return 0
}

func parseAccountUsageLimitWindow(detail string, now time.Time) (accountUsageLimitWindow, bool) {
	text := strings.TrimSpace(detail)
	if text == "" {
		return accountUsageLimitWindow{}, false
	}

	var payload accountUsageLimitErrorEnvelope
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return accountUsageLimitWindow{}, false
	}

	return buildAccountUsageLimitWindow(
		payload.Error.Type,
		payload.Error.PlanType,
		payload.Error.ResetsAt,
		payload.Error.ResetsInSeconds,
		now,
	)
}

func buildAccountUsageLimitWindow(errorType, planType string, resetsAt, resetsInSeconds int64, now time.Time) (accountUsageLimitWindow, bool) {
	if strings.TrimSpace(strings.ToLower(errorType)) != "usage_limit_reached" {
		return accountUsageLimitWindow{}, false
	}

	resetAt := time.Time{}
	switch {
	case resetsAt > 0:
		resetAt = time.Unix(resetsAt, 0)
	case resetsInSeconds > 0:
		resetAt = now.Add(time.Duration(resetsInSeconds) * time.Second)
	default:
		return accountUsageLimitWindow{}, false
	}

	if !resetAt.After(now) {
		return accountUsageLimitWindow{}, false
	}

	return accountUsageLimitWindow{
		planType: strings.TrimSpace(planType),
		resetAt:  resetAt,
	}, true
}

func readAndRestoreResponseBody(resp *http.Response, limit int64) string {
	if resp == nil || resp.Body == nil {
		return ""
	}

	peekSize := int(limit)
	if peekSize <= 0 {
		peekSize = 1024
	}
	readerSize := peekSize
	if readerSize < 256 {
		readerSize = 256
	}

	originalBody := resp.Body
	bufferedReader := bufio.NewReaderSize(originalBody, readerSize)
	visibleBody, _ := bufferedReader.Peek(peekSize)
	resp.Body = &bufferedBodyReadCloser{
		Reader: bufferedReader,
		Closer: originalBody,
	}

	return strings.TrimSpace(string(visibleBody))
}

type bufferedBodyReadCloser struct {
	io.Reader
	io.Closer
}

func (h *Handler) getAccountHTTPClient(isSSE bool) (*http.Client, error) {
	h.accountHTTPInitOnce.Do(func() {
		if h.config == nil {
			h.accountHTTPInitErr = fmt.Errorf("handler config is nil")
			return
		}

		httpTransport, err := transport.CreateTransport(h.config)
		if err != nil {
			h.accountHTTPInitErr = fmt.Errorf("failed to create shared account transport: %w", err)
			return
		}

		httpTransport.DisableKeepAlives = false
		httpTransport.MaxIdleConns = 100
		httpTransport.MaxIdleConnsPerHost = 16
		httpTransport.MaxConnsPerHost = 32
		httpTransport.IdleConnTimeout = 90 * time.Second
		httpTransport.TLSHandshakeTimeout = 10 * time.Second
		httpTransport.ExpectContinueTimeout = 1 * time.Second
		httpTransport.DisableCompression = true
		httpTransport.WriteBufferSize = 4096
		httpTransport.ReadBufferSize = 4096
		responseHeaderTimeout := h.config.Streaming.ResponseHeaderTimeout
		if responseHeaderTimeout == 0 {
			responseHeaderTimeout = 60 * time.Second
		}
		httpTransport.ResponseHeaderTimeout = responseHeaderTimeout

		timeout := h.config.GlobalTimeout
		if timeout <= 0 {
			timeout = 300 * time.Second
		}

		h.accountHTTPTransport = httpTransport
		h.accountHTTPClient = &http.Client{
			Timeout:   timeout,
			Transport: httpTransport,
		}
		h.accountSSEHTTPClient = &http.Client{
			Timeout:   0,
			Transport: httpTransport,
		}
	})

	if h.accountHTTPInitErr != nil {
		return nil, h.accountHTTPInitErr
	}
	if isSSE {
		return h.accountSSEHTTPClient, nil
	}
	return h.accountHTTPClient, nil
}

func resolveAccountTargetURL(acc *store.UpstreamAccountRecord, path, rawQuery string) (string, error) {
	if acc != nil && accountauth.IsChatGPTOAuthProvider(acc.ProviderType) {
		targetURL := ""
		switch {
		case path == "/v1/responses/compact" || strings.HasSuffix(path, "/compact"):
			targetURL = chatGPTCodexCompactURL
		case path == "/v1/models":
			targetURL = chatGPTCodexModelsURL
		case path == codexStandaloneSearchPath:
			targetURL = chatGPTCodexSearchURL
		default:
			targetURL = chatGPTCodexResponsesURL
		}
		upstreamURL, err := url.Parse(targetURL)
		if err != nil {
			return "", err
		}
		upstreamURL.RawQuery = rawQuery
		return upstreamURL.String(), nil
	}
	baseURL := ""
	if acc != nil {
		baseURL = acc.BaseURL
	}
	return joinBaseURLAndPath(baseURL, path, rawQuery)
}

func (h *Handler) processAccountRegularResponse(w http.ResponseWriter, resp *http.Response, lifecycleManager *RequestLifecycleManager, endpointName string, r *http.Request) error {
	defer resp.Body.Close()

	responseBytes, err := h.responseProcessor.ProcessResponseBody(resp)
	if err != nil {
		lifecycleManager.FailRequest("account_response_read_error", err.Error(), http.StatusBadGateway)
		return fmt.Errorf("failed to read upstream response: %w", err)
	}

	if len(responseBytes) == 0 && h.isAccountPipelinePath(r.URL.Path) {
		lifecycleManager.FailRequest("empty_response_body", "Upstream returned 2xx with empty body", http.StatusBadGateway)
		return fmt.Errorf("empty response body")
	}
	lifecycleManager.RecordNonStreamingResponseComplete()

	// v6.3：先提取响应模型 / 思考信息再写回客户端，写入失败的结算仍保留已收到的诊断字段。
	analysis := h.tokenAnalyzer.AnalyzeResponseUnified(responseBytes, lifecycleManager.GetRequestID(), endpointName)
	applyResponseAnalysisDiagnostics(lifecycleManager, analysis, "账号常规响应解析")

	h.responseProcessor.CopyResponseHeaders(resp, w)
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(responseBytes); err != nil {
		lifecycleManager.FailRequest("account_response_write_error", err.Error(), resp.StatusCode)
		return fmt.Errorf("failed to write response: %w", err)
	}

	tokenUsage := analysis.TokenUsage
	if tokenUsage != nil {
		lifecycleManager.CompleteRequest(tokenUsage)
		return nil
	}

	// 复用入口已写入 lifecycle 的分类；生成响应缺少 usage 不等于上报零用量。
	state := lifecycleManager.snapshotState()
	if state.requestSource == tracking.RequestSourceCodexSubscriptionProxy &&
		(state.requestKind == tracking.RequestKindGeneration || state.requestKind == tracking.RequestKindCompaction) {
		lifecycleManager.CompleteRequest(nil)
		return nil
	}
	lifecycleManager.HandleNonTokenResponse(string(responseBytes))
	return nil
}

func (h *Handler) processAccountStreamingResponse(ctx context.Context, w http.ResponseWriter, resp *http.Response, lifecycleManager *RequestLifecycleManager, endpointName string, upstreamCancel context.CancelFunc) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		resp.Body.Close()
		lifecycleManager.FailRequest("stream_flusher_unsupported", "response writer does not support flusher", http.StatusInternalServerError)
		return fmt.Errorf("response writer does not support flusher")
	}

	// 流式正文在 ProcessStreamWithRetry 内按上游 Content-Encoding 解压后转发，
	// 下游收到的是明文：不能再回传 Content-Encoding / Content-Length，否则客户端会按压缩体解析。
	connectionNamed := connectionNamedHeaders(resp.Header)
	for key, values := range resp.Header {
		if shouldSkipAccountResponseHeader(key, connectionNamed) {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher.Flush()

	tokenParser := NewTokenParserWithUsageTracker(lifecycleManager.GetRequestID(), h.usageTracker)
	processor := NewStreamProcessor(tokenParser, h.usageTracker, w, flusher, lifecycleManager.GetRequestID(), endpointName)
	processor.SetStreamTimingRecorders(lifecycleManager.RecordFirstTokenAndReturn, lifecycleManager.RecordStreamCompletion)
	if upstreamCancel != nil {
		processor.EnableDownstreamTailDrain(h.accountStreamTailDrainTimeout(), upstreamCancel)
	}

	tokenUsage, modelName, err := processor.ProcessStreamWithRetry(ctx, resp)
	applyStreamResponseDiagnostics(lifecycleManager, processor)
	if modelName != "" && modelName != "unknown" {
		lifecycleManager.SetModelWithComparison(modelName, "账号流式响应解析")
	}
	if err != nil {
		if streamErr, ok := err.(handlers.StreamIncompleteErrorInterface); ok {
			parsedModelName := streamErr.GetModelName()
			failureReason := streamErr.GetFailureReason()

			if parsedModelName != "unknown" && parsedModelName != "" && parsedModelName != "default" {
				lifecycleManager.SetModelWithComparison(parsedModelName, "account_stream_incomplete")
			} else if modelName != "unknown" && modelName != "" && modelName != "default" {
				lifecycleManager.SetModelWithComparison(modelName, "account_stream_processor")
			}

			lifecycleManager.CompleteRequestWithQuality(tokenUsage, failureReason)
			return nil
		}

		status, parsedModelName := parseAccountStreamStatusError(err)
		if parsedModelName != "unknown" && parsedModelName != "" && parsedModelName != "default" {
			lifecycleManager.SetModelWithComparison(parsedModelName, "account_stream_status")
		} else if modelName != "unknown" && modelName != "" && modelName != "default" {
			lifecycleManager.SetModelWithComparison(modelName, "account_stream_processor")
		}

		if status == "cancelled" {
			lifecycleManager.CancelRequest("account stream processing cancelled", tokenUsage)
			return context.Canceled
		}

		if tokenUsage != nil {
			lifecycleManager.RecordTokensForFailedRequest(tokenUsage, "account_stream_error")
		}
		lifecycleManager.FailRequest("account_stream_error", err.Error(), http.StatusBadGateway)
		return err
	}
	lifecycleManager.CompleteRequest(tokenUsage)
	return nil
}

func accountScheduleOutcomeForCodexStreamError(err error) string {
	if status, _ := parseAccountStreamStatusError(err); status == "auth_error" {
		return svc.AccountScheduleOutcomeAuthFailed
	}
	return svc.AccountScheduleOutcomeTransientFailure
}

func (h *Handler) writeRawResponse(w http.ResponseWriter, resp *http.Response) error {
	defer resp.Body.Close()
	body, err := h.responseProcessor.ProcessResponseBody(resp)
	if err != nil {
		return err
	}
	h.responseProcessor.CopyResponseHeaders(resp, w)
	w.WriteHeader(resp.StatusCode)
	_, err = w.Write(body)
	return err
}

func applyOpenAIChatGPTOAuthHeaders(req *http.Request) {
	applyOpenAIChatGPTCommonHeaders(req)
	if req == nil {
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", openAIBetaResponsesValue)
	req.Header.Set("originator", defaultOAuthOriginator)
}

func applyOpenAIChatGPTModelsHeaders(req *http.Request) {
	applyOpenAIChatGPTCommonHeaders(req)
}

func applyOpenAIChatGPTSearchHeaders(req *http.Request) {
	applyOpenAIChatGPTCommonHeaders(req)
	if req == nil {
		return
	}
	if strings.TrimSpace(req.Header.Get("originator")) == "" {
		req.Header.Set("originator", defaultOAuthOriginator)
	}
}

func applyOpenAIChatGPTCommonHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Host = "chatgpt.com"
	req.Header.Set("Host", "chatgpt.com")
}

func joinBaseURLAndPath(baseURL, path, rawQuery string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return "", err
	}
	u.RawQuery = rawQuery
	return u.String(), nil
}

func readAndCloseResponseBody(resp *http.Response, limit int64) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return strings.TrimSpace(string(raw))
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade", "content-length":
		return true
	default:
		return false
	}
}

func writeAccountPipelineError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"type":    errType,
			"message": message,
		},
	})
}

func parseAccountStreamStatusError(err error) (string, string) {
	if err == nil {
		return "error", "unknown"
	}

	var streamErr *accountStreamStatusError
	if errors.As(err, &streamErr) && streamErr != nil {
		status := strings.TrimSpace(streamErr.status)
		if status == "" {
			status = "error"
		}
		modelName := strings.TrimSpace(streamErr.modelName)
		if modelName == "" {
			modelName = "unknown"
		}
		return status, modelName
	}

	status := "error"
	modelName := "unknown"
	errText := err.Error()
	if strings.HasPrefix(errText, "stream_status:") {
		parts := strings.SplitN(errText, ":", 5)
		if len(parts) >= 4 {
			status = parts[1]
			if parts[2] == "model" && parts[3] != "" {
				modelName = parts[3]
			}
		}
	} else if strings.Contains(strings.ToLower(errText), "context canceled") {
		status = "cancelled"
	}

	return status, modelName
}

func parseAccountStreamUsageLimitWindow(err error) (accountUsageLimitWindow, bool) {
	if err == nil {
		return accountUsageLimitWindow{}, false
	}

	var streamErr *accountStreamStatusError
	if errors.As(err, &streamErr) && streamErr != nil && streamErr.usageLimit != nil {
		return *streamErr.usageLimit, true
	}
	return accountUsageLimitWindow{}, false
}

func isCancelledAccountStreamError(err error) bool {
	status, _ := parseAccountStreamStatusError(err)
	return status == "cancelled"
}
