// Package proxy - Claude Code 订阅额度的内存状态与查询时机
// 创建时间: 2026-09-28
//
// 额度有两个来源：
//  1. 每个订阅直通响应的 anthropic-ratelimit-unified-* 头：不产生额外请求，使用中实时更新 5h / 7d；
//  2. 主动查询 /api/oauth/usage：补上 claude.ai 网页等其他客户端的用量、按模型周额度与额外用量。
//
// 主动查询从严节流，全部在这里卡住，前端怎么点都绕不过：
//   - 订阅请求全部结束、静默 idleDelay 后补查一次；两次自动补查至少间隔 minInterval，不足时推迟而不是丢弃；
//   - 手动刷新两次至少间隔 manualCooldown；同一时刻只有一个查询；429 按 Retry-After 退避。
//
// 查询用的 OAuth 凭据取自最近一次订阅请求，只在内存里：最后一个请求结束 credentialTTL 后，
// 或查询返回 401/403 时丢弃；不落库、不写日志。
//
// 只有 Claude Code 入口已启用且处于订阅直通模式时才开启（App 在应用入口配置时设置，默认关闭）：
// 关闭时不记凭据、不排补查、拒绝手动刷新，切走的那一刻立即丢弃凭据并取消待发的补查；
// 已采集的读数保留，切回订阅直通后继续展示。
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"
)

const (
	claudeUsageIdleDelay      = 3 * time.Minute  // 最后一个订阅请求结束后静默多久补查
	claudeUsageMinInterval    = 10 * time.Minute // 两次自动补查的最小间隔
	claudeUsageManualCooldown = 60 * time.Second // 手动刷新的最小间隔
	claudeUsageCredentialTTL  = 30 * time.Minute // 最后一个订阅请求结束后凭据最多保留多久
	claudeUsageDefaultBackoff = 10 * time.Minute // 429 没带 Retry-After 时的退避
	claudeUsageRequestTimeout = 10 * time.Second
)

var (
	errClaudeUsageBusy         = errors.New("正在查询官方额度，结果稍后自动更新")
	errClaudeUsageNoCredential = errors.New("没有可用的登录凭据：凭据只在最近一次订阅直通请求后保留 30 分钟，请先发起一次订阅请求")
	errClaudeUsageInactive     = errors.New("订阅直通未启用：只有 Claude Code 入口已启用且处于订阅直通模式时才查询额度")
)

// claudeUsageCooldownError 手动刷新仍在冷却或退避中
type claudeUsageCooldownError struct{ wait time.Duration }

func (e *claudeUsageCooldownError) Error() string {
	return fmt.Sprintf("刷新过于频繁，请 %d 秒后再试", int(math.Ceil(e.wait.Seconds())))
}

type claudeUsageCredential struct {
	authorization string
	userAgent     string
}

type claudeUsagePolicy struct {
	idleDelay      time.Duration
	minInterval    time.Duration
	manualCooldown time.Duration
	credentialTTL  time.Duration
	defaultBackoff time.Duration
	requestTimeout time.Duration
}

var defaultClaudeUsagePolicy = claudeUsagePolicy{
	idleDelay:      claudeUsageIdleDelay,
	minInterval:    claudeUsageMinInterval,
	manualCooldown: claudeUsageManualCooldown,
	credentialTTL:  claudeUsageCredentialTTL,
	defaultBackoff: claudeUsageDefaultBackoff,
	requestTimeout: claudeUsageRequestTimeout,
}

type claudeUsageFetcher func(context.Context, claudeUsageCredential) (ClaudeSubscriptionUsage, error)

// ClaudeSubscriptionQuotaState 订阅额度的对外快照；Revision 每次状态变化递增，前端据此丢弃乱序到达的旧状态
type ClaudeSubscriptionQuotaState struct {
	Revision            uint64
	Headers             *ClaudeSubscriptionRateLimit // 最近一次带额度头的订阅响应
	Usage               *ClaudeSubscriptionUsage     // 最近一次主动查询成功的结果
	CredentialAvailable bool
	RefreshAvailableAt  *time.Time // 手动刷新最早可用时间；从未查询过且没有退避时为 nil
	LastQueriedAt       *time.Time
	LastError           string
}

type claudeSubscriptionQuota struct {
	policy claudeUsagePolicy
	fetch  claudeUsageFetcher

	mu       sync.Mutex
	onChange func(ClaudeSubscriptionQuotaState)
	// active 入口已启用且处于订阅直通模式；关闭时不记凭据、不查询
	active           bool
	revision         uint64
	headers          *ClaudeSubscriptionRateLimit
	usage            *ClaudeSubscriptionUsage
	credential       *claudeUsageCredential
	inflight         int
	lastRequestEndAt time.Time
	lastAttemptAt    time.Time
	backoffUntil     time.Time
	querying         bool
	lastError        string
	// 定时器回调带着排期时的代号，代号对不上说明已被取消或重排，直接忽略
	queryTimer  *time.Timer
	queryGen    uint64
	expiryTimer *time.Timer
	expiryGen   uint64
}

func newClaudeSubscriptionQuota(fetch claudeUsageFetcher, policy claudeUsagePolicy) *claudeSubscriptionQuota {
	return &claudeSubscriptionQuota{fetch: fetch, policy: policy}
}

// SetOnClaudeSubscriptionQuota 注册额度状态变化回调；在请求或定时器 goroutine 上调用，须尽快返回
func (h *Handler) SetOnClaudeSubscriptionQuota(fn func(ClaudeSubscriptionQuotaState)) {
	h.claudeQuota.mu.Lock()
	h.claudeQuota.onChange = fn
	h.claudeQuota.mu.Unlock()
}

// ClaudeSubscriptionQuota 返回当前额度状态
func (h *Handler) ClaudeSubscriptionQuota() ClaudeSubscriptionQuotaState {
	h.claudeQuota.mu.Lock()
	defer h.claudeQuota.mu.Unlock()
	return h.claudeQuota.stateLocked()
}

// RefreshClaudeSubscriptionQuota 手动刷新：受开关、冷却、退避与单飞限制，查询失败写在状态的 LastError 里
func (h *Handler) RefreshClaudeSubscriptionQuota() (ClaudeSubscriptionQuotaState, error) {
	return h.claudeQuota.refresh()
}

// SetClaudeSubscriptionQuotaActive 入口已启用且处于订阅直通模式时开启；应用入口配置前调用
func (h *Handler) SetClaudeSubscriptionQuotaActive(active bool) {
	h.claudeQuota.setActive(active)
}

// setActive 关闭时立即丢弃凭据、取消待发的补查与过期计时；进行中的查询照常写回结果
func (q *claudeSubscriptionQuota) setActive(active bool) {
	q.mu.Lock()
	if q.active == active {
		q.mu.Unlock()
		return
	}
	q.active = active
	dropped := false
	if !active {
		q.cancelTimerLocked(&q.queryTimer, &q.queryGen)
		q.cancelTimerLocked(&q.expiryTimer, &q.expiryGen)
		dropped = q.credential != nil
		q.credential = nil
	}
	if dropped {
		q.revision++
	}
	q.unlockAndPublish(dropped)
}

// beginRequest 订阅请求开始：开启时记下最新凭据，暂停补查与凭据过期计时
func (q *claudeSubscriptionQuota) beginRequest(authorization, userAgent string) {
	q.mu.Lock()
	q.inflight++
	record := q.active && authorization != ""
	gained := record && q.credential == nil
	if record {
		q.credential = &claudeUsageCredential{authorization: authorization, userAgent: userAgent}
	}
	q.cancelTimerLocked(&q.queryTimer, &q.queryGen)
	q.cancelTimerLocked(&q.expiryTimer, &q.expiryGen)
	if gained {
		q.revision++
	}
	q.unlockAndPublish(gained)
}

// endRequest 订阅请求结束：开启时全部结束后重新开始静默补查与凭据过期计时；
// 切走后才结束的在途请求不再排补查
func (q *claudeSubscriptionQuota) endRequest() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.inflight > 0 {
		q.inflight--
	}
	q.lastRequestEndAt = time.Now()
	if q.inflight > 0 || !q.active {
		return
	}
	q.scheduleLocked(&q.queryTimer, &q.queryGen, q.policy.idleDelay, q.onIdle)
	q.scheduleLocked(&q.expiryTimer, &q.expiryGen, q.policy.credentialTTL, q.onCredentialExpired)
}

// recordHeaders 采集响应头额度；只有带 unified-status 的响应才整份替换
func (q *claudeSubscriptionQuota) recordHeaders(header http.Header) {
	snapshot, ok := parseClaudeSubscriptionRateLimit(header, time.Now())
	if !ok {
		return
	}
	q.mu.Lock()
	q.headers = &snapshot
	q.revision++
	q.unlockAndPublish(true)
}

// onIdle 静默期满：请求结束后还没查过才补查，离上次查询太近或退避中则推迟到可查时刻
func (q *claudeSubscriptionQuota) onIdle(gen uint64) {
	q.mu.Lock()
	if gen != q.queryGen {
		q.mu.Unlock()
		return
	}
	q.queryTimer = nil
	if !q.active || q.inflight > 0 || q.credential == nil || q.lastAttemptAt.After(q.lastRequestEndAt) {
		q.mu.Unlock()
		return
	}
	if wait := time.Until(q.nextAllowedLocked(q.policy.minInterval)); wait > 0 {
		q.scheduleLocked(&q.queryTimer, &q.queryGen, wait, q.onIdle)
		q.mu.Unlock()
		return
	}
	credential := q.beginQueryLocked()
	q.mu.Unlock()
	q.runQuery(credential, "静默补查")
}

// onCredentialExpired 最后一个请求结束已满 credentialTTL，丢弃内存里的凭据
func (q *claudeSubscriptionQuota) onCredentialExpired(gen uint64) {
	q.mu.Lock()
	if gen != q.expiryGen || q.inflight > 0 || q.credential == nil {
		q.mu.Unlock()
		return
	}
	q.expiryTimer = nil
	q.credential = nil
	q.revision++
	q.unlockAndPublish(true)
}

func (q *claudeSubscriptionQuota) refresh() (ClaudeSubscriptionQuotaState, error) {
	q.mu.Lock()
	var err error
	switch {
	case !q.active:
		err = errClaudeUsageInactive
	case q.querying:
		err = errClaudeUsageBusy
	case q.credential == nil:
		err = errClaudeUsageNoCredential
	default:
		if wait := time.Until(q.nextAllowedLocked(q.policy.manualCooldown)); wait > 0 {
			err = &claudeUsageCooldownError{wait: wait}
		}
	}
	if err != nil {
		state := q.stateLocked()
		q.mu.Unlock()
		return state, err
	}
	credential := q.beginQueryLocked()
	q.mu.Unlock()
	return q.runQuery(credential, "手动刷新"), nil
}

func (q *claudeSubscriptionQuota) beginQueryLocked() claudeUsageCredential {
	q.querying = true
	q.lastAttemptAt = time.Now()
	return *q.credential
}

func (q *claudeSubscriptionQuota) runQuery(credential claudeUsageCredential, trigger string) ClaudeSubscriptionQuotaState {
	ctx, cancel := context.WithTimeout(context.Background(), q.policy.requestTimeout)
	usage, err := q.fetch(ctx, credential)
	cancel()
	if err != nil {
		slog.Warn(fmt.Sprintf("⚠️ [订阅额度] %s查询失败: %v", trigger, err))
	} else {
		slog.Info(fmt.Sprintf("📊 [订阅额度] %s完成", trigger))
	}

	q.mu.Lock()
	q.querying = false
	q.applyQueryResultLocked(credential, usage, err)
	q.revision++
	state := q.stateLocked()
	notify := q.onChange
	q.mu.Unlock()
	if notify != nil {
		notify(state)
	}
	return state
}

func (q *claudeSubscriptionQuota) applyQueryResultLocked(credential claudeUsageCredential, usage ClaudeSubscriptionUsage, err error) {
	if err == nil {
		q.usage = &usage
		q.lastError = ""
		return
	}
	var statusErr *claudeUsageStatusError
	if !errors.As(err, &statusErr) {
		q.lastError = describeClaudeUsageTransportError(err)
		return
	}
	switch statusErr.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		// 查询期间来了新请求、凭据已换新时，不能把新凭据一起丢掉
		if q.credential != nil && q.credential.authorization == credential.authorization {
			q.credential = nil
			q.cancelTimerLocked(&q.expiryTimer, &q.expiryGen)
		}
		q.lastError = "登录凭据已失效，等待下一次订阅直通请求"
	case http.StatusTooManyRequests:
		backoff := statusErr.retryAfter
		if backoff <= 0 {
			backoff = q.policy.defaultBackoff
		}
		q.backoffUntil = time.Now().Add(backoff)
		q.lastError = "官方限流，已暂停查询"
	default:
		q.lastError = "查询失败：" + statusErr.Error()
	}
}

// describeClaudeUsageTransportError 非 HTTP 状态类失败的展示文案；底层错误只进日志
func describeClaudeUsageTransportError(err error) string {
	switch {
	case errors.Is(err, errClaudeUsageEmpty):
		return err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "查询超时"
	default:
		return "查询失败，请检查网络"
	}
}

// nextAllowedLocked 下一次允许查询的时刻：距上次查询满 interval，且不在退避期
func (q *claudeSubscriptionQuota) nextAllowedLocked(interval time.Duration) time.Time {
	next := q.lastAttemptAt.Add(interval)
	if q.backoffUntil.After(next) {
		next = q.backoffUntil
	}
	return next
}

func (q *claudeSubscriptionQuota) stateLocked() ClaudeSubscriptionQuotaState {
	state := ClaudeSubscriptionQuotaState{
		Revision:            q.revision,
		CredentialAvailable: q.credential != nil,
		LastError:           q.lastError,
	}
	if q.headers != nil {
		headers := *q.headers
		state.Headers = &headers
	}
	if q.usage != nil {
		usage := *q.usage
		state.Usage = &usage
	}
	if !q.lastAttemptAt.IsZero() {
		queriedAt := q.lastAttemptAt.UTC()
		state.LastQueriedAt = &queriedAt
	}
	if !q.lastAttemptAt.IsZero() || !q.backoffUntil.IsZero() {
		availableAt := q.nextAllowedLocked(q.policy.manualCooldown).UTC()
		state.RefreshAvailableAt = &availableAt
	}
	return state
}

// unlockAndPublish 释放锁；changed 时在锁外通知前端
func (q *claudeSubscriptionQuota) unlockAndPublish(changed bool) {
	var state ClaudeSubscriptionQuotaState
	notify := q.onChange
	if changed {
		state = q.stateLocked()
	}
	q.mu.Unlock()
	if changed && notify != nil {
		notify(state)
	}
}

func (q *claudeSubscriptionQuota) scheduleLocked(timer **time.Timer, gen *uint64, delay time.Duration, fire func(uint64)) {
	q.cancelTimerLocked(timer, gen)
	current := *gen
	*timer = time.AfterFunc(delay, func() { fire(current) })
}

func (q *claudeSubscriptionQuota) cancelTimerLocked(timer **time.Timer, gen *uint64) {
	*gen++
	if *timer != nil {
		(*timer).Stop()
		*timer = nil
	}
}
