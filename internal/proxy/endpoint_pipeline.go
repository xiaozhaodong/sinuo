package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/proxy/response"
)

// 端点侧扁平转发管线（方案 §4.4）：单层候选循环替代旧 streaming/regular 双循环壳。
// 候选由 PrepareRouteCandidates 提供（无写副作用），失败分类走 §3.1 决策表，
// 响应处理返回显式 EndpointProcessOutcome，仅 FullSuccess 驱动 activeEndpoint CAS 迁移。

// EndpointProcessOutcome P2 响应处理的显式结局（替代 "err == nil 即成功"）
type EndpointProcessOutcome int

const (
	// ProcessOutcomeFullSuccess 完整成功
	ProcessOutcomeFullSuccess EndpointProcessOutcome = iota
	// ProcessOutcomeQualityIncomplete 流不完整但已完成（CompleteRequestWithQuality 路径）；不迁移 activeEndpoint
	ProcessOutcomeQualityIncomplete
	// ProcessOutcomeFailedAfterCommit 已开始输出后失败（FailRequest 路径）
	ProcessOutcomeFailedAfterCommit
	// ProcessOutcomeCancelled 客户端取消
	ProcessOutcomeCancelled
)

const (
	// endpointPassthroughRetryAfterSeconds 穿透错误的建议客户端重试等待
	endpointPassthroughRetryAfterSeconds = 5
	endpointClientErrorBodySampleLimit   = 2048
	endpointStreamTailDrainTimeout       = time.Second
)

// handleEndpointPipeline 端点转发主入口（streaming 与 regular 合并，仅 Process 分叉）
func (h *Handler) handleEndpointPipeline(ctx context.Context, w http.ResponseWriter, r *http.Request, bodyBytes []byte, lifecycleManager *RequestLifecycleManager, isSSE bool) {
	connID := lifecycleManager.GetRequestID()
	profile := endpoint.BuildRouteRequestProfile(r.URL.Path, bodyBytes)
	if profile.Model != "" {
		lifecycleManager.SetModel(profile.Model)
	}
	scope := h.endpointManager.ResolveRouteScope(profile)
	switch scope.Kind {
	case endpoint.RouteScopeUpstreamGroup:
		slog.Debug(fmt.Sprintf("🧭 [模型路由] [%s] model=%s key=%s web_tools=%v server_tools=%v -> 上游组 %s mode=%s target=%s",
			connID, scope.Model, scope.MatchedKey, profile.WebTools, profile.ServerToolTypes, scope.GroupName, scope.Mode, scope.Target))
	case endpoint.RouteScopeUnassigned:
		slog.Warn(fmt.Sprintf("🧭 [模型路由] [%s] model=%s web_tools=%v server_tools=%v 未分配上游组，返回 422", connID, scope.Model, profile.WebTools, profile.ServerToolTypes))
	}

	// reject 模式前置检查（保留现状语义）
	if shouldReject, rejectedEndpoint := h.endpointManager.ShouldRejectRequest(profile); shouldReject {
		h.endpointManager.BeginEndpointScheduleSnapshotForScope(connID, r.URL.Path, scope)
		slog.Warn(fmt.Sprintf("❌ [失败追踪] [%s] 端点 %s 达到失败阈值，拒绝请求（reject 模式）", connID, rejectedEndpoint))
		message := fmt.Sprintf("Service temporarily unavailable: endpoint %s failure threshold reached", rejectedEndpoint)
		// 拒绝前置发生在候选循环之外，SetEndpointAttempt 不会执行；
		// 不补归属则追踪记录落空上游，排障时看不出是哪个端点触发的阈值。
		// ShouldRejectRequest 的第一逻辑候选优先取 manual fixed/preferred 目标，
		// 路由上下文同样要落库，否则手动路由期间的拒绝会被读成自动调度。
		lifecycleManager.SetEndpoint(rejectedEndpoint)
		h.noteRequestRouteContext(lifecycleManager, scope, "rejected_by_failure_tracker")
		lifecycleManager.FailRequest("rejected_by_failure_tracker",
			fmt.Sprintf("Endpoint %s reached failure threshold, request rejected", rejectedEndpoint),
			http.StatusServiceUnavailable)
		h.completeEndpointSchedule(connID, scope, "", endpoint.EndpointScheduleOutcomeRejectedByFailureTracker, message)
		http.Error(w, message, http.StatusServiceUnavailable)
		return
	}

	var flusher http.Flusher
	hasCommitted := func() bool { return false }
	if isSSE {
		w, flusher, hasCommitted = handlers.NewStreamingResponseWriter(w)
		setEndpointStreamingHeaders(w)
	}

	result := h.endpointManager.PrepareRouteCandidates(ctx, profile)
	scope = result.Scope
	h.endpointManager.BeginEndpointScheduleSnapshot(connID, r.URL.Path, result.Snapshot)

	if len(result.Candidates) == 0 {
		if block := h.endpointManager.GetRouteBlock(profile); block != nil {
			routeDecision := h.endpointManager.NoteRouteDecisionForScope(scope, block.Endpoint, block.Reason)
			// 被 block 的端点是已知的，必须落进上游归属；effectiveEndpoint 仍传空——
			// 本次并未真正打到上游，两者语义不同不可合并。
			lifecycleManager.SetEndpoint(block.Endpoint)
			lifecycleManager.SetRouteDecision(routeDecision, "")
			lifecycleManager.FailRequest(block.Reason, block.Message, block.StatusCode)
			outcome := endpoint.EndpointScheduleOutcomeManualFixedBlocked
			if scope.Kind == endpoint.RouteScopeUnassigned {
				outcome = endpoint.EndpointScheduleOutcomeModelRoutingUnassigned
			}
			h.completeEndpointSchedule(connID, scope, "", outcome, block.Message)
			h.writeEndpointPipelineError(w, isSSE, flusher, block.StatusCode,
				fmt.Sprintf("%s: %s", block.Code, block.Message), block.RetryAfter)
			return
		}
		retryAfter := h.endpointRetryAfterSeconds(result.Snapshot)
		// 候选为空时没有端点可归属，但路由模式必须落真实值：
		// 不补则 route_mode 落库为默认 auto，manual_preferred/manual_fixed 期间的失败会被误读成自动调度。
		h.noteRequestRouteContext(lifecycleManager, scope, "no_routable_candidates")
		lifecycleManager.FailRequest("no_endpoints_available", "no routable endpoint candidates", http.StatusServiceUnavailable)
		h.completeEndpointSchedule(connID, scope, "", endpoint.EndpointScheduleOutcomeNoCandidates, "no routable endpoint candidates")
		h.writeEndpointPipelineError(w, isSSE, flusher, http.StatusServiceUnavailable,
			"Service Unavailable: no routable endpoint candidates", retryAfter)
		return
	}

	maxCandidates := h.config.Failover.MaxCandidateAttempts
	if maxCandidates <= 0 {
		maxCandidates = 3
	}

	var lastErr error
	attemptedEndpoints := 0
	// admission lease 只覆盖当前候选的 attempt：切换下一候选时立即释放上一轮 lease，
	// 避免失败候选的 lease 持有到请求结束、无意义拖住停用/删除的 drain 等待；
	// defer 兜底释放最后一轮（Release 幂等且 nil 安全，覆盖 panic 与提前 return）
	var admission *endpoint.AttemptAdmission
	defer func() { admission.Release() }()
	for i := range result.Candidates {
		admission.Release()
		plan := result.Plans[i]
		// §8.5 请求内尝试预算：最多尝试 maxCandidates 个不同端点
		if attemptedEndpoints >= maxCandidates {
			slog.Warn(fmt.Sprintf("⛔ [端点管线] [%s] 尝试预算耗尽（%d/%d），剩余 %d 个候选不再尝试",
				connID, attemptedEndpoints, maxCandidates, len(result.Candidates)-i))
			if lastErr != nil {
				lastErr = fmt.Errorf("candidate attempt budget exhausted (%d): %w", maxCandidates, lastErr)
			} else {
				lastErr = fmt.Errorf("candidate attempt budget exhausted (%d)", maxCandidates)
			}
			break
		}
		select {
		case <-ctx.Done():
			lifecycleManager.CancelRequest("client disconnected", nil)
			h.completeEndpointSchedule(connID, scope, "", endpoint.EndpointScheduleOutcomeCancelled, "client disconnected")
			*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", 499))
			if isSSE && hasCommitted() {
				fmt.Fprintf(w, "data: cancelled: 客户端取消请求\n\n")
				flusher.Flush()
			}
			return
		default:
		}

		// 临时 Venlacy 平滑限速必须发生在 admission 之前：排队不能占住 lease，
		// 到达发送时隙后仍由下方现有准入逻辑重新校验删除、停用与配置 revision。
		queuedFor, paceErr := h.waitForEndpointDispatch(ctx, plan.EndpointName)
		if paceErr != nil {
			lifecycleManager.CancelRequest("client disconnected while waiting for endpoint dispatch", nil)
			h.completeEndpointSchedule(connID, scope, "", endpoint.EndpointScheduleOutcomeCancelled, "client disconnected while waiting for endpoint dispatch")
			*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", 499))
			return
		}
		if queuedFor >= time.Millisecond {
			slog.Info("⏱️ [Venlacy临时限速] 请求已按出站间隔放行", "request_id", connID, "endpoint", plan.EndpointName, "wait", queuedFor)
		}

		// §14.2：attempt 前原子重校验（删除 / pending gate / hard disable / config revision）
		var acquireErr error
		admission, acquireErr = h.endpointManager.AcquireEndpointAttempt(plan)
		if acquireErr != nil {
			slog.Warn(fmt.Sprintf("⏭️ [端点管线] [%s] 候选 %s 跳过: %s", connID, plan.EndpointName, acquireErr.Error()))
			h.endpointManager.RecordEndpointScheduleAttempt(connID, plan.EndpointName, endpoint.EndpointScheduleRuntimeTryNext, acquireErr.Error())
			continue
		}
		attemptedEndpoints++
		target := admission.Target
		// 后续状态结算沿用 Endpoint 形态，但实例来自 admission 的独立配置副本，
		// 不再触碰 Manager 内的可变 Endpoint。
		ep := &endpoint.Endpoint{Config: target.Config()}

		lifecycleManager.SetEndpointAttempt(ep.Config.Name, target.Revision())
		h.endpointManager.RecordEndpointScheduleAttempt(connID, ep.Config.Name, endpoint.EndpointScheduleRuntimeAttempting, "")
		routeDecision := h.endpointManager.NoteRouteDecisionForScope(scope, ep.Config.Name, "")
		lifecycleManager.SetRouteDecision(routeDecision, ep.Config.Name)
		lifecycleManager.UpdateStatus("forwarding", i, 0)
		*r = *r.WithContext(context.WithValue(r.Context(), "selected_endpoint", ep.Config.Name))

		resp, upstreamCancel, traceState, forwardErr := h.forwarder.ForwardForPipeline(
			ctx, r, bodyBytes, target, isSSE, isSSE && shouldEnableEndpointTailDrain(r), lifecycleManager.SetFirstTokenStartTime)
		recordEndpointUpstreamModel(lifecycleManager, traceState)
		releaseUpstream := func() {
			if upstreamCancel != nil {
				upstreamCancel()
				upstreamCancel = nil
			}
		}

		// 🛡️ 隐私策略短路：本地策略拒绝不是上游失败，不换候选、不标记端点
		if policyErr := handlers.AsPrivacyPolicyError(forwardErr); policyErr != nil {
			releaseUpstream()
			slog.Warn(fmt.Sprintf("🛡️ [隐私保护] [%s] 请求被策略拒绝: %s", connID, policyErr.Code))
			*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", policyErr.StatusCode))
			lifecycleManager.FailRequest(handlers.PrivacyFailureReason(policyErr), policyErr.Message, policyErr.StatusCode)
			h.completeEndpointSchedule(connID, scope, ep.Config.Name, endpoint.EndpointScheduleOutcomePrivacyBlocked, policyErr.Message)
			h.writeEndpointPipelineError(w, isSSE, flusher, policyErr.StatusCode,
				fmt.Sprintf("%s: %s", policyErr.Code, policyErr.Message), 0)
			return
		}

		// 4xx 分类需要 body 样本（窥读并复原，供模型不支持 / schema 不兼容判定）
		bodySample := ""
		if forwardErr == nil && resp != nil && resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
			bodySample = readAndRestoreResponseBody(resp, endpointClientErrorBodySampleLimit)
		}
		decision := decideEndpointForwardOutcome(forwardErr, resp, traceState, bodySample)
		lifecycleManager.IncrementAttempt()

		// §9.2 普通 429 同端点短重试（最多 1 次；gate 未取得或等待超限则跳过）
		if decision.Action == EndpointForwardRateLimited {
			if delay, ok := h.rateLimitRetryDelay(ep.Config.Name, decision.RetryAfter); ok && !hasCommitted() && ctx.Err() == nil {
				if release := h.tryAcquireRateLimitRetryGate(ep.Config.Name, r.URL.Path); release != nil {
					closeEndpointResponse(resp)
					releaseUpstream()
					// 首次 attempt 已结束；等待期不占用 admission，重试在真正发送前重新准入。
					admission.Release()
					admission = nil
					if sleepWithContext(ctx, delay) {
						pacedWait, paceErr := h.waitForEndpointDispatch(ctx, plan.EndpointName)
						if paceErr == nil {
							retryAdmission, retryAcquireErr := h.endpointManager.AcquireEndpointAttempt(plan)
							if retryAcquireErr == nil {
								admission = retryAdmission
								target = admission.Target
								ep = &endpoint.Endpoint{Config: target.Config()}
								slog.Info(fmt.Sprintf("🔁 [429短重试] [%s] 端点 %s 等待 %s 后同端点重试", connID, ep.Config.Name, delay+pacedWait))
								resp, upstreamCancel, traceState, forwardErr = h.forwarder.ForwardForPipeline(
									ctx, r, bodyBytes, target, isSSE, isSSE && shouldEnableEndpointTailDrain(r), lifecycleManager.SetFirstTokenStartTime)
								recordEndpointUpstreamModel(lifecycleManager, traceState)
								releaseUpstream = func() {
									if upstreamCancel != nil {
										upstreamCancel()
										upstreamCancel = nil
									}
								}
								bodySample = ""
								if forwardErr == nil && resp != nil && resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
									bodySample = readAndRestoreResponseBody(resp, endpointClientErrorBodySampleLimit)
								}
								decision = decideEndpointForwardOutcome(forwardErr, resp, traceState, bodySample)
								lifecycleManager.IncrementAttempt()
							}
						}
					}
					release()
				}
			}
		}

		switch decision.Action {
		case EndpointForwardRateLimited:
			// 429 等待期间客户端取消：按取消终态处理，不计软失败、不回写 429（§9.2）
			if ctx.Err() != nil && !hasCommitted() {
				closeEndpointResponse(resp)
				releaseUpstream()
				lifecycleManager.CancelRequest("client disconnected", nil)
				h.completeEndpointSchedule(connID, scope, ep.Config.Name, endpoint.EndpointScheduleOutcomeCancelled, "client disconnected during rate limit wait")
				*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", 499))
				return
			}
			// 最终仍为普通 429：一个客户端请求只结算一次 rate_limit 软失败（§9.2 规则 7）
			detail := endpointFailureDetail(forwardErr, resp, ep.Config.Name)
			retryAfterSeconds := 0
			if decision.RetryAfter > 0 {
				retryAfterSeconds = int(decision.RetryAfter.Seconds())
				if retryAfterSeconds < 1 {
					retryAfterSeconds = 1
				}
			}
			closeEndpointResponse(resp)
			releaseUpstream()
			count, tripped := 0, false
			applied := h.endpointManager.ApplyEndpointFailureSettlement(ep.Config.Name, target.Revision(), target.FailureEpoch(), func() {
				count, tripped, _ = h.endpointManager.RecordSoftFailureFenced(ep.Config.Name, endpoint.SoftFailureScopeMessages, endpoint.SoftFailureCategoryRateLimit, target.FailureEpoch())
				if tripped {
					cooldown := endpointSoftFailureCooldown(h.config, endpoint.SoftFailureCategoryRateLimit, decision.RetryAfter)
					if h.endpointManager.SetEndpointCooldownFenced(ep.Config.Name, cooldown, endpoint.SoftFailureCooldownReason(endpoint.SoftFailureCategoryRateLimit), target.FailureEpoch()) {
						slog.Warn(fmt.Sprintf("🧊 [限流冷却] [%s] 端点 %s 达阈值进入冷却 %s", connID, ep.Config.Name, cooldown))
					}
				}
			})
			if applied {
				slog.Warn(fmt.Sprintf("⏳ [限流软失败] [%s] 端点 %s rate_limit 计数 %d/%d",
					connID, ep.Config.Name, count, h.endpointManager.SoftFailureThreshold(ep.Config.Name)))
			} else {
				slog.Info("跳过旧配置 attempt 的限流结算", "endpoint", ep.Config.Name, "revision", target.Revision())
			}
			if tripped {
				// §9.2 规则 9：阈值触发可在本次请求内换下一候选（action=reject 时不换）
				if h.config.FailureTracker.Action != "reject" && i+1 < len(result.Candidates) && attemptedEndpoints < maxCandidates && ctx.Err() == nil {
					lastErr = fmt.Errorf("%s: %s", decision.Reason, detail)
					h.endpointManager.RecordEndpointScheduleAttempt(connID, ep.Config.Name, endpoint.EndpointScheduleRuntimeTryNext, detail)
					h.notifyFailover(FailoverEvent{
						Lane:         FailoverLaneCC,
						From:         ep.Config.Name,
						To:           result.Plans[i+1].EndpointName,
						ReasonCode:   decision.Reason,
						ReasonDetail: failoverHTTPDetail(http.StatusTooManyRequests, detail),
						RequestID:    connID,
						RequestPath:  r.URL.Path,
						Attempt:      i + 1,
					})
					continue
				}
			}
			// §9.2 规则 8：阈值前（或无可用候选）返回规范化 429，保留有效 Retry-After
			*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", http.StatusTooManyRequests))
			lifecycleManager.FailRequest("rate_limited", detail, http.StatusTooManyRequests)
			h.completeEndpointSchedule(connID, scope, ep.Config.Name, endpoint.EndpointScheduleOutcomeRateLimited, detail)
			h.writeEndpointPipelineError(w, isSSE, flusher, http.StatusTooManyRequests,
				"Too Many Requests: upstream rate limited", retryAfterSeconds)
			return

		case EndpointForwardNextCandidate:
			detail := endpointFailureDetail(forwardErr, resp, ep.Config.Name)
			closeEndpointResponse(resp)
			releaseUpstream()
			lastErr = fmt.Errorf("%s: %s", decision.Reason, detail)
			h.markEndpointFailure(ep.Config.Name, target.Revision(), target.FailureEpoch(), decision, profile, detail)
			h.endpointManager.RecordEndpointScheduleAttempt(connID, ep.Config.Name, endpoint.EndpointScheduleRuntimeTryNext, detail)
			if i+1 < len(result.Candidates) && ctx.Err() == nil {
				statusCode := 0
				if resp != nil {
					statusCode = resp.StatusCode
				}
				h.notifyFailover(FailoverEvent{
					Lane:         FailoverLaneCC,
					From:         ep.Config.Name,
					To:           result.Plans[i+1].EndpointName,
					ReasonCode:   decision.Reason,
					ReasonDetail: failoverHTTPDetail(statusCode, detail),
					RequestID:    connID,
					RequestPath:  r.URL.Path,
					Attempt:      i + 1,
				})
			}
			slog.Warn(fmt.Sprintf("🔄 [端点管线] [%s] 端点 %s 失败（%s），换下一候选 (%d/%d)",
				connID, ep.Config.Name, decision.Reason, i+1, len(result.Candidates)))
			continue

		case EndpointForwardPassthroughError:
			detail := endpointFailureDetail(forwardErr, resp, ep.Config.Name)
			closeEndpointResponse(resp)
			releaseUpstream()
			h.markEndpointFailure(ep.Config.Name, target.Revision(), target.FailureEpoch(), decision, profile, detail)
			// 「真实码→500 + Retry-After」客户端重试控制转换仅此一份（§3.1）
			*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", http.StatusInternalServerError))
			lifecycleManager.FailRequest(decision.Reason, detail, http.StatusInternalServerError)
			h.completeEndpointSchedule(connID, scope, ep.Config.Name, endpoint.EndpointScheduleOutcomePassthroughError, detail)
			h.writeEndpointPipelineError(w, isSSE, flusher, http.StatusInternalServerError,
				fmt.Sprintf("upstream failure (%s): %s", decision.Reason, detail), endpointPassthroughRetryAfterSeconds)
			return

		case EndpointForwardPassthroughRaw:
			detail := fmt.Sprintf("upstream returned %d", resp.StatusCode)
			relayErr := h.relayEndpointRawResponse(w, resp, lifecycleManager, isSSE, flusher)
			releaseUpstream()
			if relayErr != nil {
				h.completeEndpointSchedule(connID, scope, ep.Config.Name, endpoint.EndpointScheduleOutcomePassthroughError, relayErr.Error())
			} else {
				h.completeEndpointSchedule(connID, scope, ep.Config.Name, endpoint.EndpointScheduleOutcomePassthroughRaw, detail)
			}
			return

		default: // EndpointForwardProcess
			var outcome EndpointProcessOutcome
			var outcomeDetail string
			if isSSE {
				outcome, outcomeDetail = h.processEndpointStreamingResponse(ctx, w, r, resp, flusher, lifecycleManager, ep.Config.Name, target.Revision(), target.FailureEpoch(), upstreamCancel)
			} else {
				outcome, outcomeDetail = h.processEndpointRegularResponse(w, r, resp, lifecycleManager, ep.Config.Name)
			}
			releaseUpstream()
			h.completeEndpointSchedule(connID, scope, ep.Config.Name, endpointScheduleOutcomeName(outcome), outcomeDetail)
			// v8 §11.2：FullSuccess 只更新运行时 retained，不再持久化 active（D9）；
			// count_tokens 不建立 /v1/messages retained（§10 规则 6）
			if outcome == ProcessOutcomeFullSuccess && r.URL.Path == "/v1/messages" {
				retained := false
				h.endpointManager.ApplyEndpointAttemptSettlement(ep.Config.Name, target.Revision(), func() {
					retained = h.endpointManager.UpdateAutoRetentionForScope(
						scope, ep.Config.Name, ep.Config.Priority, plan.SelectionSource,
					)
				})
				if retained {
					slog.Debug("同层 retained 已更新", "endpoint", ep.Config.Name, "priority", ep.Config.Priority)
				}
			}
			return
		}
	}

	// 全部候选失败：503 + Retry-After（快照 availableAt）
	reason := "all endpoint candidates failed"
	if lastErr != nil {
		reason = lastErr.Error()
	}
	retryAfter := h.endpointRetryAfterSeconds(result.Snapshot)
	lifecycleManager.FailRequest("all_endpoints_failed", reason, http.StatusServiceUnavailable)
	h.completeEndpointSchedule(connID, scope, "", endpoint.EndpointScheduleOutcomeAllCandidatesFailed, reason)
	h.writeEndpointPipelineError(w, isSSE, flusher, http.StatusServiceUnavailable,
		"Service Unavailable: "+reason, retryAfter)
}

// noteRequestRouteContext 只写本请求的路由诊断快照，不触碰全局路由状态。
// 前置短路分支（reject / 无候选）没有选中端点，effectiveEndpoint 恒为空；
// 刻意不走 NoteRouteDecision——那会清空全局 LastEffectiveEndpoint 并 notify 前端，
// 把一次请求的诊断升级成全局状态写。
func (h *Handler) noteRequestRouteContext(lifecycleManager *RequestLifecycleManager, scope endpoint.RouteScope, fallbackReason string) {
	route := h.endpointManager.RouteStateForScope(scope)
	route.FallbackReason = fallbackReason
	route.LastDecisionAt = time.Now()
	lifecycleManager.SetRouteDecision(route, "")
}

func (h *Handler) completeEndpointSchedule(requestID string, scope endpoint.RouteScope, endpointName, outcome, finalError string) {
	finalSnapshot := h.endpointManager.CompleteEndpointScheduleSnapshot(requestID, endpointName, outcome, finalError)
	h.persistEndpointScheduleSnapshot(requestID, finalSnapshot)
	h.endpointManager.SettleRouteDecision(scope, endpointName, outcome)
}

// markEndpointFailure 按决策标记端点状态（§9.1 状态处理列）。
// 429（EndpointForwardRateLimited）在管线内单独结算，不走本函数。
func (h *Handler) markEndpointFailure(name string, revision int64, epoch int64, decision EndpointFailureDecision, profile endpoint.RouteRequestProfile, detail string) {
	switch decision.Mark {
	case EndpointMarkSoftFailure:
		h.recordEndpointAttemptSoftFailure(name, revision, epoch, decision.Category, decision.RetryAfter, decision.Reason)
	case EndpointMarkAuthCooldown:
		h.endpointManager.ApplyEndpointFailureSettlement(name, revision, epoch, func() {
			cooldown := h.config.Failover.AuthCooldown
			if cooldown <= 0 {
				cooldown = 30 * time.Minute
			}
			if h.endpointManager.SetEndpointCooldownFenced(name, cooldown, decision.Reason, epoch) {
				slog.Warn(fmt.Sprintf("🔑 [鉴权冷却] 端点 %s 进入长冷却（%s）: %s", name, cooldown, detail))
			}
		})
	case EndpointMarkNegativeCache:
		h.endpointManager.ApplyEndpointAttemptSettlement(name, revision, func() {
			h.endpointManager.RecordNegativeRouteHit(name, string(decision.FailureClass), profile, detail)
			slog.Info(fmt.Sprintf("🧭 [路由负缓存] 端点 %s 记录 %s（不计软失败窗口）", name, decision.FailureClass))
		})
	}
}

// recordEndpointAttemptSoftFailure 记录带 config revision fencing 的 messages scope 软失败。
func (h *Handler) recordEndpointAttemptSoftFailure(name string, revision int64, epoch int64, category endpoint.SoftFailureCategory, retryAfter time.Duration, reason string) (int, bool) {
	count, tripped := 0, false
	h.endpointManager.ApplyEndpointFailureSettlement(name, revision, epoch, func() {
		count, tripped, _ = h.endpointManager.RecordSoftFailureFenced(name, endpoint.SoftFailureScopeMessages, category, epoch)
		slog.Info(fmt.Sprintf("📊 [软失败] 端点 %s 记录 %s（%s），窗口内计数: %d/%d",
			name, category, reason, count, h.endpointManager.SoftFailureThreshold(name)))
		if tripped {
			cooldown := endpointSoftFailureCooldown(h.config, category, retryAfter)
			if h.endpointManager.SetEndpointCooldownFenced(name, cooldown, endpoint.SoftFailureCooldownReason(category), epoch) {
				slog.Warn(fmt.Sprintf("🧊 [软失败冷却] 端点 %s %s 达阈值，冷却 %s", name, category, cooldown))
			}
		}
	})
	return count, tripped
}

func (h *Handler) rateLimitRetryDelay(endpointName string, retryAfter time.Duration) (time.Duration, bool) {
	if !h.config.Failover.RateLimitRetryEnabled() {
		return 0, false
	}
	// Venlacy 的无 Retry-After 429 不再追加 250-750ms 随机等待；统一回到
	// 9 秒出站节奏，避免短重试本身继续冲击每分钟 7 请求的上游限制。
	if isTemporaryPacedEndpoint(endpointName) && retryAfter <= 0 {
		return 0, true
	}
	maxWait := h.config.Failover.RateLimitRetry.MaxWait
	if maxWait <= 0 {
		maxWait = 2 * time.Second
	}
	if retryAfter > maxWait {
		return 0, false // 超过上限不在服务端阻塞等待
	}
	if retryAfter > 0 {
		return retryAfter, true
	}
	// 无 Retry-After：250ms-750ms 带 jitter 短等待
	return 250*time.Millisecond + time.Duration(rand.Int63n(int64(500*time.Millisecond))), true
}

// tryAcquireRateLimitRetryGate 非阻塞获取 endpoint+path 的 429 重试 gate（§9.2 规则 5）；
// 返回 nil 表示未取得（并发请求跳过本地等待）。
func (h *Handler) tryAcquireRateLimitRetryGate(endpointName, path string) func() {
	key := endpointName + "|" + path
	value, _ := h.rateLimitRetryGates.LoadOrStore(key, new(int32))
	flag := value.(*int32)
	if !atomic.CompareAndSwapInt32(flag, 0, 1) {
		return nil
	}
	return func() { atomic.StoreInt32(flag, 0) }
}

// sleepWithContext 可被客户端取消中断的等待；返回 false 表示已取消
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// processEndpointStreamingResponse P2 流式响应处理（自旧循环壳迁移，内部逻辑只搬不改；
// 新增仅为 EndpointProcessOutcome 返回值包装）
func (h *Handler) processEndpointStreamingResponse(ctx context.Context, w http.ResponseWriter, r *http.Request, resp *http.Response, flusher http.Flusher, lifecycleManager *RequestLifecycleManager, endpointName string, endpointRevision int64, endpointEpoch int64, upstreamCancel context.CancelFunc) (EndpointProcessOutcome, string) {
	connID := lifecycleManager.GetRequestID()
	lifecycleManager.UpdateStatus("processing", lifecycleManager.GetAttemptCount(), resp.StatusCode)
	w.WriteHeader(resp.StatusCode)

	tokenParser := NewTokenParserWithUsageTracker(connID, h.usageTracker)
	processor := NewStreamProcessor(tokenParser, h.usageTracker, w, flusher, connID, endpointName)
	processor.SetStreamTimingRecorders(lifecycleManager.RecordFirstTokenAndReturn, lifecycleManager.RecordStreamCompletion)
	if upstreamCancel != nil {
		processor.EnableDownstreamTailDrain(endpointStreamTailDrainTimeout, upstreamCancel)
	}

	slog.Info(fmt.Sprintf("🚀 [开始流式处理] [%s] 端点: %s", connID, endpointName))
	finalTokenUsage, modelName, err := processor.ProcessStreamWithRetry(ctx, resp)
	applyStreamResponseDiagnostics(lifecycleManager, processor)
	if err != nil {
		if streamErr, ok := err.(handlers.StreamIncompleteErrorInterface); ok {
			// 流不完整但请求已完成：按完成记账并标记 failure_reason（不计失败、不迁移 active）
			parsedModelName := streamErr.GetModelName()
			failureReason := streamErr.GetFailureReason()
			if parsedModelName != "unknown" && parsedModelName != "" && parsedModelName != "default" {
				lifecycleManager.SetModelWithComparison(parsedModelName, "stream_incomplete")
			} else if modelName != "unknown" && modelName != "" && modelName != "default" {
				lifecycleManager.SetModelWithComparison(modelName, "stream_processor")
			}
			lifecycleManager.CompleteRequestWithQuality(finalTokenUsage, failureReason)
			slog.Info(fmt.Sprintf("⚠️ [流不完整但已完成] [%s] 端点: %s, failure_reason: %s",
				connID, endpointName, failureReason))
			return ProcessOutcomeQualityIncomplete, failureReason
		}

		status, parsedModelName := "error", "unknown"
		if strings.HasPrefix(err.Error(), "stream_status:") {
			parts := strings.SplitN(err.Error(), ":", 5)
			if len(parts) >= 4 {
				status = parts[1]
				if parts[2] == "model" && parts[3] != "" {
					parsedModelName = parts[3]
				}
			}
		}
		if parsedModelName != "unknown" && parsedModelName != "" {
			lifecycleManager.SetModelWithComparison(parsedModelName, "stream_status")
		} else if modelName != "unknown" && modelName != "" {
			lifecycleManager.SetModelWithComparison(modelName, "stream_processor")
		}

		lifecycleManager.HandleError(err)
		statusCode := handlers.GetStatusCodeFromError(err, resp)
		if status == "error" || status == "stream_error" {
			statusCode = http.StatusMultiStatus
		} else if status == "cancelled" {
			statusCode = 499
		}

		if status == "cancelled" {
			lifecycleManager.CancelRequest("stream processing cancelled", finalTokenUsage)
			*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", statusCode))
			fmt.Fprintf(w, "data: cancelled: 客户端取消请求\n\n")
			flusher.Flush()
			return ProcessOutcomeCancelled, err.Error()
		}

		if finalTokenUsage != nil {
			lifecycleManager.RecordTokensForFailedRequest(finalTokenUsage, status)
		}
		lifecycleManager.FailRequest(status, err.Error(), statusCode)
		*r = *r.WithContext(context.WithValue(r.Context(), "final_status_code", statusCode))
		slog.Warn(fmt.Sprintf("🔄 [流式处理失败] [%s] 端点: %s, 状态: %s, 错误: %v", connID, endpointName, status, err))

		// 📊 [软失败] P2 流式失败计入 messages+transport（§9.1，取消除外——已提前返回）
		h.recordEndpointAttemptSoftFailure(endpointName, endpointRevision, endpointEpoch, endpoint.SoftFailureCategoryTransport, 0, "stream_failure")

		if h.config.Streaming.EOFRetryHint {
			interruptModelName := parsedModelName
			if interruptModelName == "" || interruptModelName == "unknown" {
				interruptModelName = modelName
			}
			if interruptModelName == "" || interruptModelName == "unknown" {
				interruptModelName = "claude-3-5-sonnet-20241022"
			}
			slog.Info(fmt.Sprintf("🔄 [流式错误重试提示] [%s] 发送中断消息触发客户端重试, 模型: %s", connID, interruptModelName))
			handlers.SendStreamInterruptedMessage(w, flusher, "Connection interrupted", interruptModelName)
		} else {
			fmt.Fprintf(w, "data: error: 流式处理失败: %v\n\n", err)
			flusher.Flush()
		}
		return ProcessOutcomeFailedAfterCommit, err.Error()
	}

	if finalTokenUsage != nil {
		if modelName != "unknown" && modelName != "" {
			lifecycleManager.SetModelWithComparison(modelName, "流式响应解析")
		}
		lifecycleManager.CompleteRequest(finalTokenUsage)
	} else {
		lifecycleManager.HandleNonTokenResponse("")
	}
	return ProcessOutcomeFullSuccess, ""
}

// processEndpointRegularResponse P2 常规响应处理（自旧循环壳迁移，内部逻辑只搬不改）
func (h *Handler) processEndpointRegularResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, lifecycleManager *RequestLifecycleManager, endpointName string) (EndpointProcessOutcome, string) {
	connID := lifecycleManager.GetRequestID()
	lifecycleManager.UpdateStatus("processing", lifecycleManager.GetAttemptCount(), resp.StatusCode)
	defer resp.Body.Close()

	responseBytes, err := h.responseProcessor.ProcessResponseBody(resp)
	if err != nil {
		lifecycleManager.FailRequest("response_read_error", err.Error(), http.StatusBadGateway)
		http.Error(w, "Failed to read upstream response", http.StatusBadGateway)
		return ProcessOutcomeFailedAfterCommit, err.Error()
	}

	if len(responseBytes) == 0 && r.URL.Path == "/v1/messages" {
		slog.Error(fmt.Sprintf("❌ [空响应体] [%s] 端点 %s 返回 200 但响应体为空，判定为上游异常", connID, endpointName))
		lifecycleManager.FailRequest("empty_response_body", "Upstream returned 200 with empty body", http.StatusBadGateway)
		http.Error(w, "Upstream returned empty response", http.StatusBadGateway)
		return ProcessOutcomeFailedAfterCommit, "Upstream returned 200 with empty body"
	}
	lifecycleManager.RecordNonStreamingResponseComplete()

	// v6.3：上游响应已完整读入，先提取响应模型 / 思考信息再写回客户端，
	// 客户端断连导致写入失败时失败结算仍保留已收到的诊断字段。
	isCountTokens := r.URL.Path == "/v1/messages/count_tokens"
	var analysis response.ResponseAnalysis
	if !isCountTokens {
		analysis = h.tokenAnalyzer.AnalyzeResponseUnified(responseBytes, connID, endpointName)
		applyResponseAnalysisDiagnostics(lifecycleManager, analysis, "常规响应解析")
	}

	h.responseProcessor.CopyResponseHeaders(resp, w)
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(responseBytes); err != nil {
		// 不再经过 HandleError：断连错误会被分类为客户端取消并先落 cancelled，
		// 与随后的 FailRequest 形成双终态竞争；与账号池管线对齐，只落一次 failed。
		slog.Error("Failed to write response to client", "request_id", connID, "error", err)
		lifecycleManager.FailRequest("response_write_error", err.Error(), resp.StatusCode)
		return ProcessOutcomeFailedAfterCommit, err.Error()
	}

	if isCountTokens {
		lifecycleManager.CompleteRequest(nil)
		return ProcessOutcomeFullSuccess, ""
	}

	tokenUsage := analysis.TokenUsage
	if tokenUsage != nil {
		lifecycleManager.CompleteRequest(tokenUsage)
	} else {
		lifecycleManager.HandleNonTokenResponse(string(responseBytes))
	}
	return ProcessOutcomeFullSuccess, ""
}

// relayEndpointRawResponse 原样透传上游 4xx（客户端请求问题，不记录端点状态）
func (h *Handler) relayEndpointRawResponse(w http.ResponseWriter, resp *http.Response, lifecycleManager *RequestLifecycleManager, isSSE bool, flusher http.Flusher) error {
	detail := fmt.Sprintf("upstream returned %d", resp.StatusCode)
	if err := h.writeRawResponse(w, resp); err != nil {
		lifecycleManager.FailRequest("endpoint_response_write_error", err.Error(), resp.StatusCode)
		return err
	}
	if isSSE && flusher != nil {
		flusher.Flush()
	}
	lifecycleManager.FailRequest("upstream_client_error", detail, resp.StatusCode)
	return nil
}

func endpointScheduleOutcomeName(outcome EndpointProcessOutcome) string {
	switch outcome {
	case ProcessOutcomeFullSuccess:
		return endpoint.EndpointScheduleOutcomeSuccess
	case ProcessOutcomeQualityIncomplete:
		return endpoint.EndpointScheduleOutcomeQualityIncomplete
	case ProcessOutcomeCancelled:
		return endpoint.EndpointScheduleOutcomeCancelled
	default:
		return endpoint.EndpointScheduleOutcomeFailedAfterCommit
	}
}

// endpointRetryAfterSeconds 从调度快照的 skipped availableAt 计算 Retry-After；
// 无任何 availableAt 时用 FailureTracker 窗口兜底（§3.1 补充规则）
func (h *Handler) endpointRetryAfterSeconds(snapshot *endpoint.EndpointScheduleSnapshot) int {
	if earliest := snapshot.EarliestAvailableAt(); !earliest.IsZero() {
		if seconds := int(time.Until(earliest).Seconds()) + 1; seconds > 0 {
			return seconds
		}
		return 1
	}
	if h.config != nil && h.config.FailureTracker.TimeWindow > 0 {
		return int(h.config.FailureTracker.TimeWindow.Seconds())
	}
	return 30
}

// writeEndpointPipelineError 错误写回（SSE 与常规分叉）
func (h *Handler) writeEndpointPipelineError(w http.ResponseWriter, isSSE bool, flusher http.Flusher, statusCode int, message string, retryAfterSeconds int) {
	if retryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	}
	if isSSE && flusher != nil {
		handlers.WriteStreamingTerminalError(w, flusher, statusCode, message)
		return
	}
	http.Error(w, message, statusCode)
}

func setEndpointStreamingHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Cache-Control")
}

func shouldEnableEndpointTailDrain(r *http.Request) bool {
	return r != nil && r.URL != nil && r.URL.Path == "/v1/messages"
}

func endpointFailureDetail(forwardErr error, resp *http.Response, endpointName string) string {
	if forwardErr != nil {
		return forwardErr.Error()
	}
	if resp == nil {
		return "empty response"
	}
	if detail := handlers.BuildUpstreamErrorDetail(resp, endpointName, 512); detail != "" {
		return detail
	}
	return fmt.Sprintf("upstream returned %d", resp.StatusCode)
}

func closeEndpointResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}
