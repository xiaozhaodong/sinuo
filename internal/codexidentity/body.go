package codexidentity

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 仅解释 rust-v0.155.1 的已知直属路径，绝不递归替换用户文本或服务端 ID。
var associationKeys = map[string]string{
	"session_id": "session", "session-id": "session", "thread_id": "thread", "thread-id": "thread",
	"x-client-request-id": "request", "window_id": "window", HeaderWindowID: "window",
	"parent_thread_id": "parent", HeaderParentThreadID: "parent",
	"installation_id": "installation", HeaderInstallationID: "installation",
	"turn_id": "turn", "parent_turn_id": "parent_turn", "root_turn_id": "root_turn",
	"context_window_id": "context_window", "forked_from_thread_id": "forked_thread",
}

type bodyAssociation struct {
	values               map[string]string
	fields               map[string]json.RawMessage
	metadata             []byte
	turnHeader, turnBody []byte
}

func readAssociation(src SourceHeaders, fields map[string]json.RawMessage) (*bodyAssociation, error) {
	a := &bodyAssociation{values: map[string]string{}, fields: fields}
	for key, value := range map[string]string{"session": src.SessionID, "thread": src.ThreadID, "request": src.ClientRequestID, "window": src.WindowID, "parent": src.ParentThreadID, "installation": src.InstallationID} {
		a.values[key] = value
	}
	collect := func(raw []byte) error {
		fields, err := JSONFields(raw)
		if err != nil {
			return err
		}
		for key, value := range fields {
			logical, ok := associationKeys[key]
			if !ok || string(value) == "null" {
				continue
			}
			var text string
			if json.Unmarshal(value, &text) != nil || len(text) > maxAssociationIDLength || !isVisibleASCII(text, false) {
				return fmt.Errorf("invalid association field %s", key)
			}
			merged, err := mergeSourceValue(logical, a.values[logical], text)
			if err != nil {
				return err
			}
			a.values[logical] = merged
		}
		return nil
	}
	if len(fields) > 0 {
		if raw, exists := a.fields["client_metadata"]; exists {
			a.metadata = raw
			if err := collect(raw); err != nil {
				return nil, err
			}
			fields, _ := JSONFields(raw)
			if raw, ok := fields[HeaderTurnMetadata]; ok {
				var text string
				if json.Unmarshal(raw, &text) != nil {
					return nil, fmt.Errorf("turn metadata must be a JSON string")
				}
				a.turnBody = []byte(text)
				if err := collect(a.turnBody); err != nil {
					return nil, err
				}
			}
		}
	}
	if src.TurnMetadata != "" {
		a.turnHeader = []byte(src.TurnMetadata)
		if err := collect(a.turnHeader); err != nil {
			return nil, err
		}
	}
	// 所有显式 session 来源收集完后才用缓存键兜底；独立缓存键与 session 不同不是冲突。
	if raw, exists := a.fields["prompt_cache_key"]; exists {
		var key string
		if json.Unmarshal(raw, &key) != nil {
			return nil, fmt.Errorf("prompt_cache_key must be a string")
		}
		if a.values["session"] == "" && key != "" {
			a.values["session"] = key
		}
	}
	return a, nil
}

func (a *bodyAssociation) fillMissing(fallback string) {
	source := a.values
	if source["session"] == "" {
		source["session"] = source["thread"]
	}
	if source["session"] == "" {
		source["session"] = fallback
	}
	if source["thread"] == "" {
		source["thread"] = source["session"]
	}
	if source["request"] == "" {
		source["request"] = source["thread"]
	}
}

func (a *bodyAssociation) mapped(mapper Mapper, fallback string) map[string]string {
	a.fillMissing(fallback)
	source := a.values
	mapped := map[string]string{}
	for key, value := range source {
		mapped[key] = mapper.ID(value)
	}
	mapped["window"] = mapper.WindowID(source["window"])
	if mapped["window"] == "" {
		mapped["window"] = mapped["thread"] + ":0"
	}
	mapped["installation"] = mapper.InstallationID()
	return mapped
}

// metadataRewriter 映射 metadata 中的已知关联字段并清理首方扩展；删除的未知扩展只记录名称。
type metadataRewriter struct {
	mapped map[string]string
	owner  Owner
	index  *OwnerIndex
	out    *Prepared
}

// rewrite 保留非身份业务字段；未知首方扩展字段不进入账号池，避免新增身份字段未经映射出站。
func (w *metadataRewriter) rewrite(raw []byte, nested bool) ([]byte, error) {
	fields, err := JSONFields(raw)
	if err != nil {
		return nil, err
	}
	location := "client_metadata."
	if nested {
		location = "turn_metadata."
	}
	edits := map[string][]byte{}
	for key, value := range fields {
		if logical, ok := associationKeys[key]; ok {
			if string(value) != "null" {
				edits[key] = JSONString(w.mapped[logical])
			}
			continue
		}
		switch key {
		case HeaderTurnMetadata:
			if nested {
				return nil, fmt.Errorf("recursive turn metadata")
			}
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, fmt.Errorf("invalid turn metadata")
			}
			updated, err := w.rewrite([]byte(text), true)
			if err != nil {
				return nil, err
			}
			edits[key] = JSONString(string(updated))
		case HeaderTurnState:
			var token string
			if json.Unmarshal(value, &token) != nil || !w.index.Allows(token, w.owner, time.Now()) {
				edits[key] = nil
			}
		case HeaderSubagent:
			var subagent string
			if json.Unmarshal(value, &subagent) != nil || !validSubagent(subagent) {
				return nil, fmt.Errorf("unsupported subagent metadata")
			}
		case HeaderAttestation, "authorization", "chatgpt-account-id", "cookie", "traceparent", "tracestate":
			edits[key] = nil
		default:
			if isFirstPartyExtension(strings.ToLower(key)) {
				edits[key] = nil
				w.out.dropField(location + key)
			}
		}
	}
	return EditObject(raw, edits)
}

func identityBodyError(err error) error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return newError(CodeBodyInvalid, http.StatusBadRequest, "invalid Codex association metadata: %v", err)
}

// AssociationFingerprint 比较隐私处理前后的关联字段，不记录其明文。
// 只比较身份相关位置；用户文本与标准 metadata 不参与。
func AssociationFingerprint(body []byte) (string, error) {
	if len(body) == 0 {
		return "", nil
	}
	object, err := parseObject(body)
	if err != nil {
		return "", err
	}
	return associationFingerprintFields(object.fields)
}

func associationFingerprintFields(fields map[string]json.RawMessage) (string, error) {
	relevant := map[string]json.RawMessage{}
	for _, key := range []string{"prompt_cache_key", "client_metadata", "model", "service_tier"} {
		if value, ok := fields[key]; ok {
			relevant[key] = value
		}
	}
	// 原始 metadata 内有本地环境字段；允许隐私过滤修改它们，只提取已知关联值。
	if raw := relevant["client_metadata"]; raw != nil {
		var collect func([]byte, bool) (map[string]any, error)
		collect = func(raw []byte, nested bool) (map[string]any, error) {
			fields, err := JSONFields(raw)
			if err != nil {
				return nil, err
			}
			result := map[string]any{}
			for key, value := range fields {
				if _, ok := associationKeys[key]; ok || key == HeaderTurnState || key == HeaderAttestation {
					result[key] = value
				}
				if key == HeaderTurnMetadata {
					if nested {
						return nil, fmt.Errorf("recursive turn metadata")
					}
					var text string
					if json.Unmarshal(value, &text) != nil {
						return nil, fmt.Errorf("invalid turn metadata")
					}
					sub, err := collect([]byte(text), true)
					if err != nil {
						return nil, err
					}
					result[key] = sub
				}
			}
			return result, nil
		}
		values, err := collect(raw, false)
		if err != nil {
			return "", err
		}
		relevant["client_metadata"], _ = json.Marshal(values)
	}
	data, err := json.Marshal(relevant)
	return string(data), err
}

// ValidatePrivacyBody 用内容摘要识别未变化的正文；同内容的新切片可复用校验，
// 原地改写也会被发现。正文确有变化时重新校验并比较身份字段，不能信任过滤器的 Changed 标志。
func (p *Prepared) ValidatePrivacyBody(body []byte) error {
	if !p.IdentityEnabled || sha256.Sum256(body) == p.bodyDigest {
		return nil
	}
	after, err := AssociationFingerprint(body)
	if err != nil || after != p.bodyFingerprint {
		return newError(CodePrivacyConflict, http.StatusUnprocessableEntity, "隐私处理改变了必要的关联字段或路由字段，或生成了无效 JSON")
	}
	return nil
}
