package proxy

import (
	"fmt"
	"strings"

	"sinuo/internal/codexidentity"

	"github.com/tidwall/gjson"
)

const (
	codexInternalMetadataKey            = "internal_chat_message_metadata_passthrough"
	codexEncryptedContentKey            = "encrypted_content"
	codexOutputReplayInclude            = "output.encrypted_content"
	requestBodyCodeCodexMetadataInvalid = "codex_metadata_invalid_request"
)

// codexReplayKeepTypes 中条目的 encrypted_content 属于标准协议，必须保留；
// 其余条目上的 encrypted_content 是输出重放（output_token_replay）回放的密文。
var codexReplayKeepTypes = map[string]bool{"reasoning": true, "compaction": true}

// codexPrivateFieldStats 记录出站兼容处理的移除数量，只用于日志。
type codexPrivateFieldStats struct {
	MetadataItems  int // 移除了内部元数据的 input 条目数
	ReplayItems    int // 移除了回放输出密文的 input 条目数
	IncludeRemoved int // 从 include 移除的 output.encrypted_content 个数
}

// stripCodexPrivateFields 为只实现标准 Responses 接口的上游移除 Codex 私有字段：
// input 条目的直属内部元数据与回放输出密文，以及 include 中的 output.encrypted_content。
// 推理与压缩条目的密文保留；不修改原始请求字节，没有目标字段时直接返回原报文。
func stripCodexPrivateFields(body []byte) ([]byte, codexPrivateFieldStats, error) {
	fields, err := codexidentity.JSONFields(body)
	if err != nil {
		return nil, codexPrivateFieldStats{}, err
	}
	changes := map[string][]byte{}
	var stats codexPrivateFieldStats
	if raw, exists := fields["input"]; exists {
		input, inputStats, err := stripCodexInputItems(raw)
		if err != nil {
			return nil, codexPrivateFieldStats{}, err
		}
		if input != nil {
			changes["input"] = input
		}
		stats = inputStats
	}
	if raw, exists := fields["include"]; exists {
		if include, removed := stripCodexReplayInclude(raw); removed > 0 {
			changes["include"] = include
			stats.IncludeRemoved = removed
		}
	}
	if len(changes) == 0 {
		return body, stats, nil
	}
	out, err := codexidentity.EditObject(body, changes)
	if err != nil {
		return nil, codexPrivateFieldStats{}, err
	}
	return out, stats, nil
}

// stripCodexInputItems 逐条移除 input 条目的直属私有字段；没有改动时返回 nil。
// RawMessage 保留大整数、密文和未知字段，只替换发生变化的条目。
func stripCodexInputItems(raw []byte) ([]byte, codexPrivateFieldStats, error) {
	var stats codexPrivateFieldStats
	input := gjson.ParseBytes(raw)
	if input.Type == gjson.Null || input.Type == gjson.String {
		return nil, stats, nil
	}
	if !input.IsArray() {
		return nil, stats, fmt.Errorf("input 必须是数组、字符串或 null")
	}
	var result []byte
	pos, edited := 0, false
	var editErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if !item.IsObject() {
			editErr = fmt.Errorf("input 条目必须是对象")
			return false
		}
		changes := codexInputItemChanges(item, &stats)
		if len(changes) == 0 {
			return true
		}
		updated, err := codexidentity.EditObject([]byte(item.Raw), changes)
		if err != nil {
			editErr = err
			return false
		}
		result = append(result, raw[pos:item.Index]...)
		result = append(result, updated...)
		pos, edited = item.Index+len(item.Raw), true
		return true
	})
	if editErr != nil {
		return nil, codexPrivateFieldStats{}, editErr
	}
	if !edited {
		return nil, stats, nil
	}
	return append(result, raw[pos:]...), stats, nil
}

// codexInputItemChanges 返回条目要删除的直属字段，并累计移除数量。
func codexInputItemChanges(item gjson.Result, stats *codexPrivateFieldStats) map[string][]byte {
	changes := map[string][]byte{}
	if item.Get(codexInternalMetadataKey).Exists() {
		changes[codexInternalMetadataKey] = nil
		stats.MetadataItems++
	}
	if !codexReplayKeepTypes[item.Get("type").String()] && item.Get(codexEncryptedContentKey).Exists() {
		changes[codexEncryptedContentKey] = nil
		stats.ReplayItems++
	}
	return changes
}

// stripCodexReplayInclude 从 include 数组移除 output.encrypted_content，其余值按原文保留；
// include 不是数组或不含目标值时不改动。
func stripCodexReplayInclude(raw []byte) ([]byte, int) {
	include := gjson.ParseBytes(raw)
	if !include.IsArray() {
		return nil, 0
	}
	var kept []string
	removed := 0
	include.ForEach(func(_, value gjson.Result) bool {
		if value.Type == gjson.String && value.Str == codexOutputReplayInclude {
			removed++
		} else {
			kept = append(kept, value.Raw)
		}
		return true
	})
	if removed == 0 {
		return nil, 0
	}
	return []byte("[" + strings.Join(kept, ",") + "]"), removed
}
