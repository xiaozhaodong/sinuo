package inputcomposition

import (
	"encoding/json"
	"maps"
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
)

// Windows 持有当前模型目录的只读快照；已开始请求重试时仍使用当时的目录。
type Windows struct {
	current atomic.Pointer[map[string]int64]
}

func (w *Windows) Replace(windows map[string]int64) {
	snapshot := maps.Clone(windows)
	w.current.Store(&snapshot)
}

func (w *Windows) Snapshot() func(string) int64 {
	windows := w.current.Load()
	if windows == nil || len(*windows) == 0 {
		return nil
	}
	return func(model string) int64 { return (*windows)[model] }
}

// WithWindow 只替换窗口快照，重试期间无需再次解析请求正文。
func (s Summary) WithWindow(model string, limit int64, source string) Summary {
	s.Window = limit
	if limit > 0 {
		s.WindowSource = source
		s.WindowModel = model
	} else {
		s.WindowSource = ""
		s.WindowModel = ""
	}
	return s
}

// MessagesModelWindow 取厂商公布的模型容量。兼容 Messages 的端点可能运行不同厂商的模型。
// 客户端附加的 [1m] 标签不能保证服务端一定开启 1M 上下文，因此先剥离再按已知规格匹配；
// 若精确名称未命中，则尝试去掉末尾日期快照后缀（-YYYYMMDD / -YYYY-MM-DD）回退到基准型号。
// https://api-docs.deepseek.com/api/list-models/
// https://platform.claude.com/docs/en/build-with-claude/context-windows
func MessagesModelWindow(model string) int64 {
	id := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]")
	if limit := messagesModelWindowByID(id); limit > 0 {
		return limit
	}
	if base, ok := trimDateSnapshotSuffix(id); ok {
		return messagesModelWindowByID(base)
	}
	return 0
}

func messagesModelWindowByID(id string) int64 {
	switch id {
	case "deepseek-flash", "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp":
		return 1_048_576
	case "claude-fable-5-2", "claude-mythos-5-2", "claude-fable-5-1", "claude-mythos-5-1",
		"claude-fable-5", "claude-mythos-5", "claude-mythos-preview",
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6",
		"claude-sonnet-5-5", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-5-5":
		return 1_000_000
	case "claude-haiku-4-5", "claude-haiku-4-5-20251001", "claude-sonnet-4-5", "claude-sonnet-4-5-20250929",
		"claude-opus-4-5", "claude-opus-4-5-20251101", "claude-opus-4-1", "claude-opus-4-1-20250805",
		"claude-opus-4-0", "claude-opus-4-20250514", "claude-sonnet-4-0", "claude-sonnet-4-20250514":
		return 200_000
	}
	return 0
}

func trimDateSnapshotSuffix(id string) (string, bool) {
	if n := len(id); n > 9 && id[n-9] == '-' && digits(id[n-8:]) {
		return id[:n-9], true
	}
	if n := len(id); n > 11 && id[n-11] == '-' && id[n-6] == '-' && id[n-3] == '-' &&
		digits(id[n-10:n-6]) && digits(id[n-5:n-3]) && digits(id[n-2:]) {
		return id[:n-11], true
	}
	return "", false
}

// WithHistoricalMessagesWindow 只补充详情视图。保留原归档及扩展字段，已有窗口快照优先。
func WithHistoricalMessagesWindow(raw string, models ...string) (string, error) {
	if raw == "" {
		return raw, nil
	}
	model := ""
	for _, candidate := range models {
		if candidate = strings.TrimSpace(candidate); candidate != "" && candidate != "unknown" && candidate != "default" {
			model = candidate
			break
		}
	}
	limit := MessagesModelWindow(model)
	if limit == 0 {
		return raw, nil
	}
	summary := gjson.Parse(raw)
	if summary.Get("version").Int() != 1 || summary.Get("window").Exists() || summary.Get("window_source").Exists() {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", err
	}
	fields["window"], _ = json.Marshal(limit)
	fields["window_model"], _ = json.Marshal(model)
	fields["window_source"] = json.RawMessage(`"historical_model_spec"`)
	data, err := json.Marshal(fields)
	return string(data), err
}
