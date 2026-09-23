package codexidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Policy 是请求级值快照；namespace 失败只影响需要稳定映射的首方请求。
type Policy struct {
	Enabled        bool
	Automatic      bool // 启用时是否仅对官方 OAuth 目标生效；由唯一的 mode 设置解码
	Version        string
	Namespace      Namespace
	NamespaceError error
}

// IdentityMode 决定身份补全是否按本次实际出站账号自动启用。
type IdentityMode string

const (
	IdentityModeAuto     IdentityMode = "auto"
	IdentityModeDisabled IdentityMode = "disabled"
	IdentityModeEnabled  IdentityMode = "enabled"
)

// Mode 返回请求快照中已解码的配置模式。
func (p Policy) Mode() IdentityMode {
	if !p.Enabled {
		return IdentityModeDisabled
	}
	if p.Automatic {
		return IdentityModeAuto
	}
	return IdentityModeEnabled
}

// EffectiveEnabled 按最终账号作用域计算；自动模式仅对官方 OAuth 目标生效。
func (p Policy) EffectiveEnabled(scope AccountScope) bool {
	return p.Enabled && (!p.Automatic || scope.IsFirstParty())
}

type Request struct {
	Policy   Policy
	Source   SourceHeaders
	fallback string
	bodyMu   sync.Mutex
	body     *requestBodyValidation
}

type requestBodyValidation struct {
	raw    []byte
	object *jsonObject
	err    error
}

// 只缓存本请求的当前正文版本；内容比较不依赖指针，原地修改或换号改写后必须重新校验。
func (r *Request) validatedBody(raw []byte) (*jsonObject, error) {
	r.bodyMu.Lock()
	defer r.bodyMu.Unlock()
	if r.body == nil || !bytes.Equal(raw, r.body.raw) {
		snapshot := bytes.Clone(raw)
		object, err := parseObject(snapshot)
		r.body = &requestBodyValidation{raw: snapshot, object: object, err: err}
	}
	return r.body.object, r.body.err
}

func NewRequest(policy Policy, source SourceHeaders) *Request {
	return &Request{Policy: policy, Source: source, fallback: uuid.NewString()}
}

type Prepared struct {
	Body              []byte
	Headers           http.Header
	Owner             Owner
	FirstParty        bool
	IdentityEnabled   bool
	Version           string
	DescriptionSource string
	// 以下两项只含名称、不含值，供调用方限频告警：透传的未验证 beta 值，以及按策略删除的未知首方扩展字段。
	UnknownBetaFeatures []string
	DroppedFields       []string
	bodyFields          map[string]json.RawMessage // 仅构造期间使用，完成小字段指纹后释放
	bodyFingerprint     string
	bodyDigest          [sha256.Size]byte
}

func (p *Prepared) editBody(object *jsonObject, changes map[string][]byte) error {
	body, fields, err := object.edit(changes)
	if err != nil {
		return err
	}
	// 无编辑时底层 helper 返回原切片；缓存快照不得暴露给可能原地修改正文的过滤器。
	if len(body) > 0 && &body[0] == &object.raw[0] {
		body = bytes.Clone(body)
	}
	p.Body, p.bodyFields = body, fields
	return nil
}

// rust-v0.155.1 同时支持 Other(label) 与 Internal(source)，不按账号 plan 推断权限。
func validSubagent(s string) bool {
	return s != "" && len(s) <= maxAssociationIDLength && isVisibleASCII(s, false)
}

func (r *Request) Prepare(scope AccountScope, op Operation, body []byte, owners *OwnerIndex) (*Prepared, error) {
	if !r.Policy.EffectiveEnabled(scope) {
		return r.prepareUnmapped(scope, body, owners)
	}
	out, err := r.prepareMapped(scope, op, body, owners)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 && out.bodyFields == nil {
		object, err := r.validatedBody(body)
		if err != nil {
			return nil, identityBodyError(err)
		}
		out.bodyFields = object.fields
	}
	if len(out.Body) > 0 {
		out.bodyFingerprint, err = associationFingerprintFields(out.bodyFields)
		if err != nil {
			return nil, identityBodyError(err)
		}
	}
	out.bodyFields = nil
	out.bodyDigest = sha256.Sum256(out.Body)
	return out, nil
}

func (r *Request) prepareMapped(scope AccountScope, op Operation, body []byte, owners *OwnerIndex) (*Prepared, error) {
	out := &Prepared{Body: body, Headers: make(http.Header), FirstParty: scope.IsFirstParty(), IdentityEnabled: true}
	if !out.FirstParty {
		out.Headers.Set(HeaderClientRequestID, uuid.NewSHA1(uuid.NameSpaceOID, []byte(scope.Key()+r.fallback)).String())
		if len(body) > 0 {
			object, err := r.validatedBody(body)
			if err != nil {
				return nil, identityBodyError(err)
			}
			err = out.editBody(object, map[string][]byte{"client_metadata": nil})
			if err != nil {
				return nil, identityBodyError(err)
			}
		}
		return out, nil
	}
	if len(r.Source.Invalid) > 0 {
		return nil, newError(CodeInvalidAssociation, 400, "invalid Codex source headers: %s", strings.Join(r.Source.Invalid, ", "))
	}
	profile, err := BuiltinProfile(r.Policy.Version)
	if err != nil {
		return nil, newError(CodeCompatibilityInvalid, 503, "invalid Codex compatibility profile")
	}
	out.Headers.Set("User-Agent", profile.UserAgent(op))
	out.Headers.Set(HeaderOriginator, profile.Originator)
	out.Headers.Set(HeaderVersion, profile.Version)
	out.Version = profile.Version
	out.DescriptionSource = "builtin"
	// 仅已知客户端的完整描述组合作为出站描述；描述识别不参与功能声明的放行判断。
	src := r.Source
	// 未识别首方扩展头会被 ApplyHeaders（订阅进池则在重建来源头时）删除；复制避免与请求快照共享底层数组。
	out.DroppedFields = slices.Clone(src.Unrecognized)
	if knownDescriptor(src) {
		out.Headers.Set("User-Agent", src.UserAgent)
		out.Headers.Set(HeaderOriginator, src.Originator)
		out.Headers.Set(HeaderVersion, src.Version)
		out.Version = src.Version
		out.DescriptionSource = "request"
	}
	accept := "application/json"
	if op == OperationModels {
		accept = "*/*"
	}
	out.Headers.Set("Accept", accept)
	if op == OperationResponses {
		out.Headers.Set("Accept", "text/event-stream")
	}
	if op == OperationModels || op == OperationAuxiliary {
		return out, nil
	}
	if r.Policy.NamespaceError != nil || r.Policy.Namespace.IsZero() {
		return nil, newError(CodeNamespaceUnavailable, 503, "Codex identity namespace unavailable")
	}

	var object *jsonObject
	if len(body) > 0 {
		var err error
		object, err = r.validatedBody(body)
		if err != nil {
			return nil, identityBodyError(err)
		}
		out.bodyFields = object.fields
	}
	a, err := readAssociation(src, out.bodyFields)
	if err != nil {
		return nil, identityBodyError(err)
	}
	mapper := r.Policy.Namespace.Mapper(scope)
	mapped := a.mapped(mapper, r.fallback)
	out.Owner = Owner{scope.Key(), a.values["session"], a.values["thread"], ProfileRuleVersion + "/" + r.Policy.Version + "/" + r.Policy.Namespace.String()}
	rewriter := &metadataRewriter{mapped: mapped, owner: out.Owner, index: owners, out: out}
	for header, logical := range map[string]string{HeaderSessionID: "session", HeaderThreadID: "thread", HeaderClientRequestID: "request", HeaderWindowID: "window", HeaderParentThreadID: "parent"} {
		if value := mapped[logical]; value != "" {
			out.Headers.Set(header, value)
		}
	}
	if owners.Allows(src.TurnState, out.Owner, time.Now()) {
		out.Headers.Set(HeaderTurnState, src.TurnState)
	}
	if len(a.turnHeader) > 0 {
		updated, err := rewriter.rewrite(a.turnHeader, true)
		if err != nil {
			return nil, identityBodyError(err)
		}
		out.Headers.Set(HeaderTurnMetadata, string(updated))
	}
	if op == OperationResponses {
		if err := out.applyFeatureHeaders(src); err != nil {
			return nil, err
		}
	}
	if len(body) == 0 {
		return out, nil
	}
	edits := map[string][]byte{}
	if len(a.metadata) > 0 {
		updated, err := rewriter.rewrite(a.metadata, false)
		if err != nil {
			return nil, identityBodyError(err)
		}
		// 补齐已知关联字段，与对应头使用同一映射；键名对齐 rust-v0.155.1 client_metadata() 顶层，
		// installation 用 x-codex-installation-id，嵌套 turn metadata 才用 installation_id。
		updated, err = EditObject(updated, map[string][]byte{"session_id": JSONString(mapped["session"]), "thread_id": JSONString(mapped["thread"]), HeaderWindowID: JSONString(mapped["window"]), HeaderInstallationID: JSONString(mapped["installation"])})
		if err != nil {
			return nil, identityBodyError(err)
		}
		edits["client_metadata"] = updated
	}
	if raw, ok := a.fields["prompt_cache_key"]; ok {
		var key string
		if json.Unmarshal(raw, &key) != nil {
			return nil, newError(CodeBodyInvalid, 400, "prompt_cache_key must be a string")
		}
		edits["prompt_cache_key"] = JSONString(mapper.PromptCacheKey(key, a.values["parent"]))
	}
	err = out.editBody(object, edits)
	if err != nil {
		return nil, identityBodyError(err)
	}
	if op == OperationResponses {
		var model, tier string
		_ = json.Unmarshal(a.fields["model"], &model)
		_ = json.Unmarshal(a.fields["service_tier"], &tier)
		if model != "" && isVisibleASCII(model, false) && !strings.ContainsAny(model, ";=") && isVisibleASCII(tier, false) && !strings.ContainsAny(tier, ";=") {
			hint := "model=" + model
			if tier != "" {
				hint += ";tier=" + tier
			}
			out.Headers.Set(HeaderRoutingHint, hint)
		}
	}
	return out, nil
}

// ApplyHeaders 清除来源首方扩展，再写入经过目标策略生成的字段。凭据由 accountauth 最后绑定。
func (p *Prepared) ApplyHeaders(header http.Header, target *url.URL) {
	for key := range header {
		lower := strings.ToLower(key)
		if isFirstPartyExtension(lower) || lower == "originator" || lower == "version" || lower == "session-id" || lower == "thread-id" || lower == "openai-beta" || lower == "chatgpt-account-id" || lower == "x-responsesapi-include-timing-metrics" || lower == "x-client-request-id" {
			delete(header, key)
		}
	}
	for key, values := range p.Headers {
		header[key] = append([]string(nil), values...)
	}
	if p.FirstParty && target != nil && strings.HasSuffix(target.Path, "/models") {
		q := target.Query()
		q.Set("client_version", p.Version)
		target.RawQuery = q.Encode()
	}
}
