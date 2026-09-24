// Package endpoint - v8 不可变尝试计划与 attempt admission（收敛方案 §8.1 / §14.2）
// 调度输出携带值语义的计划快照；每次 attempt（首个候选、fallback、同端点 429 重试）
// 发起前都必须通过 AcquireEndpointAttempt 原子重校验并取得 admission lease。
package endpoint

import (
	"fmt"
	"time"

	"cc-forwarder/config"
)

// EndpointAttemptPlan 单个候选的不可变计划快照
type EndpointAttemptPlan struct {
	EndpointName        string        `json:"endpoint_name"`
	Priority            int           `json:"priority"`
	URL                 string        `json:"url"`
	Timeout             time.Duration `json:"timeout"`
	SupportsCountTokens bool          `json:"supports_count_tokens"`
	ConfigRevision      int64         `json:"config_revision"`
	FailureEpoch        int64         `json:"failure_epoch"`    // 快照时刻的故障 epoch;手动解除冷却会推进,旧 epoch 晚到故障结算被拒
	SelectionSource     string        `json:"selection_source"` // auto_priority / auto_retained / manual_preferred / manual_fixed / fallback
	// 仅供进程内 admission 使用；私有字段不会进入 JSON 或 Wails 模型。
	resolvedToken  string
	resolvedAPIKey string
}

// EndpointAttemptTarget 是 admission 时生成的不可变出站快照。
// 字段保持私有；Config 每次返回独立副本，调用方无法修改已取得的快照。
type EndpointAttemptTarget struct {
	name                     string
	url                      string
	priority                 int
	timeout                  time.Duration
	headers                  map[string]string
	token                    string
	apiKey                   string
	authMode                 string
	supportsCountTokens      bool
	modelRewriteRules        string
	softFailureWindowMinutes *int
	softFailureThreshold     *int
	configRevision           int64
	failureEpoch             int64
}

func (t *EndpointAttemptTarget) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

func (t *EndpointAttemptTarget) Priority() int {
	if t == nil {
		return 0
	}
	return t.priority
}

func (t *EndpointAttemptTarget) SupportsCountTokens() bool {
	return t != nil && t.supportsCountTokens
}

func (t *EndpointAttemptTarget) Revision() int64 {
	if t == nil {
		return 0
	}
	return t.configRevision
}

// FailureEpoch 返回 admission 快照捕获时刻的故障运行态 epoch。
// 旧 epoch 的晚到故障结算被写时 fencing 拒绝,不污染手动解除冷却后的运行态。
func (t *EndpointAttemptTarget) FailureEpoch() int64 {
	if t == nil {
		return 0
	}
	return t.failureEpoch
}

// Config 返回仅包含本次转发所需字段的独立配置副本。
// 多凭据列表已收敛为 admission 时选中的单个凭据，避免转发阶段再次读取运行态。
func (t *EndpointAttemptTarget) Config() config.EndpointConfig {
	if t == nil {
		return config.EndpointConfig{}
	}
	return config.EndpointConfig{
		Name:                     t.name,
		URL:                      t.url,
		Priority:                 t.priority,
		Timeout:                  t.timeout,
		Headers:                  cloneStringMap(t.headers),
		Token:                    t.token,
		ApiKey:                   t.apiKey,
		AuthMode:                 t.authMode,
		SupportsCountTokens:      t.supportsCountTokens,
		ModelRewriteRules:        t.modelRewriteRules,
		SoftFailureWindowMinutes: config.CloneSoftFailureValue(t.softFailureWindowMinutes),
		SoftFailureThreshold:     config.CloneSoftFailureValue(t.softFailureThreshold),
	}
}

// AttemptAdmission 一次 attempt 的准入结果；Release 必须以 defer 方式调用，
// 覆盖 panic、context 取消与提前 return（§7.6 规则 8）。
type AttemptAdmission struct {
	Target  *EndpointAttemptTarget `json:"-"`
	release func()
}

// Release 释放 admission lease（幂等）
func (a *AttemptAdmission) Release() {
	if a != nil && a.release != nil {
		a.release()
		a.release = nil
	}
}

// AcquireEndpointAttempt 在发起 attempt 前原子重校验安全状态（§14.2）：
// 端点存在、未 hard disabled、无 pending disable/delete gate、config revision 未变化。
// 先登记 admission 再校验：校验失败立即回退计数，保证「gate 设置后新 attempt
// 必然失败」与「校验通过的 attempt 必然被 WaitAdmissionsDrained 观测到」无竞态窗口。
// 校验失败返回带原因的 error，调用方跳过该候选并记录原因。
func (m *Manager) AcquireEndpointAttempt(plan EndpointAttemptPlan) (*AttemptAdmission, error) {
	// 配置 generation barrier 防止 admission 重校验期间发布新的 Config/revision；
	// 活动凭据已在规划阶段冻结到 plan，此处不再回查运行态。
	m.endpointConfigMu.RLock()
	defer m.endpointConfigMu.RUnlock()

	// 在 endpoints 读锁内先登记 admission，保证并发 RemoveEndpoint 要么先完成、
	// 要么能观测到本次 lease；端点名称读取同时受端点锁保护。
	m.endpointsMu.RLock()
	var ep *Endpoint
	for _, candidate := range m.endpoints {
		candidate.mutex.RLock()
		matches := candidate.Config.Name == plan.EndpointName
		candidate.mutex.RUnlock()
		if matches {
			ep = candidate
			ep.admissions.Add(1)
			break
		}
	}
	m.endpointsMu.RUnlock()
	if ep == nil {
		return nil, fmt.Errorf("endpoint_deleted")
	}

	reject := func(reason string) (*AttemptAdmission, error) {
		ep.admissions.Add(-1)
		return nil, fmt.Errorf("%s", reason)
	}

	if m.HasPendingAvailabilityGate(plan.EndpointName) {
		return reject("pending_disable_gate")
	}
	ep.mutex.RLock()
	hardEnabled := ep.Config.IsAvailabilityEnabled()
	revision := ep.configRevision
	configSnapshot := cloneEndpointConfig(ep.Config)
	ep.mutex.RUnlock()
	if !hardEnabled {
		return reject("availability_disabled")
	}
	// revision 在端点装配时即初始化为非零值，快照与运行时严格相等才放行
	if revision != plan.ConfigRevision {
		return reject("config_changed_since_snapshot")
	}
	ep.mutex.RLock()
	currentRevision := ep.configRevision
	ep.mutex.RUnlock()
	if currentRevision != revision {
		return reject("config_changed_during_admission")
	}
	target := &EndpointAttemptTarget{
		name:                     configSnapshot.Name,
		url:                      configSnapshot.URL,
		priority:                 configSnapshot.Priority,
		timeout:                  configSnapshot.Timeout,
		headers:                  cloneStringMap(configSnapshot.Headers),
		token:                    plan.resolvedToken,
		apiKey:                   plan.resolvedAPIKey,
		authMode:                 configSnapshot.AuthMode,
		supportsCountTokens:      configSnapshot.SupportsCountTokens,
		modelRewriteRules:        configSnapshot.ModelRewriteRules,
		softFailureWindowMinutes: config.CloneSoftFailureValue(configSnapshot.SoftFailureWindowMinutes),
		softFailureThreshold:     config.CloneSoftFailureValue(configSnapshot.SoftFailureThreshold),
		configRevision:           revision,
		failureEpoch:             plan.FailureEpoch,
	}

	released := false
	return &AttemptAdmission{
		Target: target,
		release: func() {
			if !released {
				released = true
				ep.admissions.Add(-1)
			}
		},
	}, nil
}

// ApplyEndpointAttemptSettlement 仅当端点仍是 attempt 对应的配置 revision 时结算运行态。
// 回调在配置 generation 读锁内执行，不得调用任何配置发布/删除入口。
// revision 不匹配时仍可由调用方记录请求与调度诊断，但不会污染新配置的运行态。
func (m *Manager) ApplyEndpointAttemptSettlement(endpointName string, expectedRevision int64, apply func()) bool {
	if m == nil || endpointName == "" || expectedRevision <= 0 || apply == nil {
		return false
	}
	m.endpointConfigMu.RLock()
	defer m.endpointConfigMu.RUnlock()

	ep := m.GetEndpointByNameAny(endpointName)
	if ep == nil {
		return false
	}
	ep.mutex.RLock()
	currentRevision := ep.configRevision
	ep.mutex.RUnlock()
	if currentRevision != expectedRevision {
		return false
	}
	apply()
	return true
}

// ApplyEndpointFailureSettlement 故障写入专用结算入口:配置 revision CAS + 故障
// epoch 检查,任一不匹配则跳过整个结算(调用方记日志"跳过过期故障结算")。
// 注意:本检查是早退优化;正确性由回调内各写时 fenced 方法保证(入口检查通过
// 后到实际写入之间 Reset 仍可插入,写时 fencing 会再次校验)。
// 回调在配置 generation 读锁内执行,不得调用任何配置发布/删除入口。
func (m *Manager) ApplyEndpointFailureSettlement(endpointName string, expectedRevision, expectedEpoch int64, apply func()) bool {
	if m == nil || endpointName == "" || expectedRevision <= 0 || apply == nil {
		return false
	}
	m.endpointConfigMu.RLock()
	defer m.endpointConfigMu.RUnlock()

	ep := m.GetEndpointByNameAny(endpointName)
	if ep == nil {
		return false
	}
	ep.mutex.RLock()
	currentRevision := ep.configRevision
	currentEpoch := ep.failureEpoch
	ep.mutex.RUnlock()
	if currentRevision != expectedRevision || currentEpoch != expectedEpoch {
		return false
	}
	apply()
	return true
}

func cloneEndpointConfig(src config.EndpointConfig) config.EndpointConfig {
	clone := src
	clone.SoftFailureWindowMinutes = config.CloneSoftFailureValue(src.SoftFailureWindowMinutes)
	clone.SoftFailureThreshold = config.CloneSoftFailureValue(src.SoftFailureThreshold)
	clone.Headers = cloneStringMap(src.Headers)
	if src.FailoverEnabled != nil {
		value := *src.FailoverEnabled
		clone.FailoverEnabled = &value
	}
	if src.Cooldown != nil {
		value := *src.Cooldown
		clone.Cooldown = &value
	}
	if src.AvailabilityEnabled != nil {
		value := *src.AvailabilityEnabled
		clone.AvailabilityEnabled = &value
	}
	return clone
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	clone := make(map[string]string, len(src))
	for key, value := range src {
		clone[key] = value
	}
	return clone
}

// resolveAttemptCredentials 从端点值快照读取本次固定凭据。
// 返回后转发链路不再访问任何运行态凭据选择器或其他端点配置。
func (m *Manager) resolveAttemptCredentials(cfg config.EndpointConfig) (string, string) {
	return cfg.Token, cfg.ApiKey
}

// WaitAdmissionsDrained 供 disable/delete 等待已取得的 admission 退出；
// 超时后 gate 继续生效并返回 false（不允许泄漏的 lease 永久阻塞停用，§7.6 规则 8）。
func (m *Manager) WaitAdmissionsDrained(name string, timeout time.Duration) bool {
	ep := m.GetEndpointByNameAny(name)
	if ep == nil {
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ep.admissions.Load() == 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ep.admissions.Load() == 0
}
