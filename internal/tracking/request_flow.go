package tracking

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// RequestFlowSchemaVersion 标识当前持久化路径 JSON 的结构版本。
	RequestFlowSchemaVersion = 1

	// MaxFlowContacts 限制单条请求持久化保留的上游联系明细数。
	MaxFlowContacts = 32

	// MaxFlowLegs 限制单条请求持久化保留的目标切换段数。
	MaxFlowLegs = 64

	// MaxFlowJSONBytes 限制单条请求 request_flow_json 的最大编码字节数（32 KiB）。
	MaxFlowJSONBytes = 32 * 1024
)

const (
	FlowTransportHTTP = "http"
	FlowTransportWS   = "ws"

	FlowOriginClientRequest   = "client_request"
	FlowOriginServerSuccessor = "server_successor"

	FlowDeliveryDelivered       = "delivered"
	FlowDeliveryPartialOrFailed = "partial_or_failed"
	FlowDeliveryAborted         = "aborted"
)

// RequestFlowTargetSnapshot 记录单次选路时捕获的目标快照，不随最新配置变动而漂移。
type RequestFlowTargetSnapshot struct {
	UpstreamType       string `json:"upstream_type,omitempty"`
	UpstreamSourceName string `json:"upstream_source_name,omitempty"`
	UpstreamName       string `json:"upstream_name,omitempty"`
	UpstreamID         int64  `json:"upstream_id,omitempty"`
	EndpointName       string `json:"endpoint_name,omitempty"`
	RouteMode          string `json:"route_mode,omitempty"`
	UpstreamModel      string `json:"upstream_model,omitempty"`
}

// RequestFlowLeg 表示一次目标选择对应的执行段，目标切换产生新段。
type RequestFlowLeg struct {
	LegSeq           int                       `json:"leg_seq"`
	TargetSnapshot   RequestFlowTargetSnapshot `json:"target_snapshot"`
	SelectedOffsetMs int64                     `json:"selected_offset_ms"`
	ReasonCode       string                    `json:"reason_code,omitempty"`
}

// RequestFlowContact 表示一次真实的联系上游尝试（含同目标重试与传输重发）。
type RequestFlowContact struct {
	ContactSeq                int    `json:"contact_seq"`
	LegSeq                    int    `json:"leg_seq"`
	StartOffsetMs             int64  `json:"start_offset_ms"`
	ConnReadyOffsetMs         *int64 `json:"conn_ready_offset_ms,omitempty"`
	WriteStartOffsetMs        *int64 `json:"write_start_offset_ms,omitempty"`
	WriteDoneOffsetMs         *int64 `json:"write_done_offset_ms,omitempty"`
	ResponseHeadersOffsetMs   *int64 `json:"response_headers_offset_ms,omitempty"`
	FirstUpstreamByteOffsetMs *int64 `json:"first_upstream_byte_offset_ms,omitempty"`
	EndOffsetMs               *int64 `json:"end_offset_ms,omitempty"`
	HTTPStatusCode            *int   `json:"http_status_code,omitempty"`
	OutcomeCode               string `json:"outcome_code,omitempty"`
	TransportResend           bool   `json:"transport_resend,omitempty"`
}

// RequestFlow 保存单条请求的有界持久化执行路径与交付里程碑。
type RequestFlow struct {
	SchemaVersion                  int                  `json:"schema_version"`
	Transport                      string               `json:"transport,omitempty"`
	Origin                         string               `json:"origin,omitempty"`
	Legs                           []RequestFlowLeg     `json:"legs,omitempty"`
	Contacts                       []RequestFlowContact `json:"contacts,omitempty"`
	TotalLegs                      int                  `json:"total_legs,omitempty"`
	TotalContacts                  int                  `json:"total_contacts,omitempty"`
	FirstClientWriteOffsetMs       *int64               `json:"first_client_write_offset_ms,omitempty"`
	UpstreamTerminalOffsetMs       *int64               `json:"upstream_terminal_offset_ms,omitempty"`
	UpstreamTerminalObserved       bool                 `json:"upstream_terminal_observed,omitempty"`
	DownstreamDeliveryDoneOffsetMs *int64               `json:"downstream_delivery_done_offset_ms,omitempty"`
	DownstreamDeliveryOutcome      string               `json:"downstream_delivery_outcome,omitempty"`
	DownstreamBytesWritten         int64                `json:"downstream_bytes_written,omitempty"`
	DownstreamFlushError           bool                 `json:"downstream_flush_error,omitempty"`
	Truncated                      bool                 `json:"truncated,omitempty"`
	OmittedContacts                int                  `json:"omitted_contacts,omitempty"`
	OmittedLegs                    int                  `json:"omitted_legs,omitempty"`
	IncompleteReason               string               `json:"incomplete_reason,omitempty"`
}

// RequestIntegrity 描述单条请求的路径记录完整性、截断和兼容性信息。
type RequestIntegrity struct {
	RecordMissingFlow bool   `json:"record_missing_flow,omitempty"`
	CorruptFlowJSON   bool   `json:"corrupt_flow_json,omitempty"`
	UnsupportedSchema bool   `json:"unsupported_schema,omitempty"`
	Truncated         bool   `json:"truncated,omitempty"`
	OmittedContacts   int    `json:"omitted_contacts,omitempty"`
	OmittedLegs       int    `json:"omitted_legs,omitempty"`
	IncompleteReason  string `json:"incomplete_reason,omitempty"`
	WriteGap          bool   `json:"write_gap,omitempty"`
	WriteGapCode      string `json:"write_gap_code,omitempty"`
	DiagnosticCode    string `json:"diagnostic_code,omitempty"`
	DiagnosticMessage string `json:"diagnostic_message,omitempty"`
}

// FlowEventKind 定义可序列化、可在热池和数据库事务中共同重放的离散路径事件。
type FlowEventKind string

const (
	FlowEventInit                   FlowEventKind = "init"
	FlowEventLegSelected            FlowEventKind = "leg_selected"
	FlowEventContactStart           FlowEventKind = "contact_start"
	FlowEventContactConnReady       FlowEventKind = "contact_conn_ready"
	FlowEventContactWriteStart      FlowEventKind = "contact_write_start"
	FlowEventContactWriteDone       FlowEventKind = "contact_write_done"
	FlowEventContactResponseHeaders FlowEventKind = "contact_response_headers"
	FlowEventContactFirstByte       FlowEventKind = "contact_first_byte"
	FlowEventContactEnd             FlowEventKind = "contact_end"
	FlowEventDownstreamFirstWrite   FlowEventKind = "downstream_first_write"
	FlowEventDownstreamWrite        FlowEventKind = "downstream_write"
	FlowEventUpstreamTerminal       FlowEventKind = "upstream_terminal"
	FlowEventDownstreamDone         FlowEventKind = "downstream_done"
	FlowEventMarkIncomplete         FlowEventKind = "mark_incomplete"
)

// RequestFlowEvent 表示单条无歧义的路径事实增量。
type RequestFlowEvent struct {
	Kind            FlowEventKind              `json:"kind"`
	At              *time.Time                 `json:"at,omitempty"`
	OffsetMs        *int64                     `json:"offset_ms,omitempty"`
	Transport       string                     `json:"transport,omitempty"`
	Origin          string                     `json:"origin,omitempty"`
	LegSeq          int                        `json:"leg_seq,omitempty"`
	ForceNewLeg     bool                       `json:"force_new_leg,omitempty"`
	Target          *RequestFlowTargetSnapshot `json:"target,omitempty"`
	ReasonCode      string                     `json:"reason_code,omitempty"`
	ContactSeq      int                        `json:"contact_seq,omitempty"`
	HTTPStatusCode  *int                       `json:"http_status_code,omitempty"`
	OutcomeCode     string                     `json:"outcome_code,omitempty"`
	TransportResend bool                       `json:"transport_resend,omitempty"`
	// FirstWrite 和 Done 携带截至该时刻的累计写出量；旧 DownstreamWrite 只携带尚未计入的增量。
	Bytes            int64  `json:"bytes,omitempty"`
	DeliveryOutcome  string `json:"delivery_outcome,omitempty"`
	FlushError       bool   `json:"flush_error,omitempty"`
	IncompleteReason string `json:"incomplete_reason,omitempty"`
}

// CloneRequestFlow 深拷贝 RequestFlow 及其内嵌切片与指针。
func CloneRequestFlow(src *RequestFlow) *RequestFlow {
	if src == nil {
		return nil
	}
	dst := &RequestFlow{
		SchemaVersion:             src.SchemaVersion,
		Transport:                 src.Transport,
		Origin:                    src.Origin,
		TotalLegs:                 src.TotalLegs,
		TotalContacts:             src.TotalContacts,
		UpstreamTerminalObserved:  src.UpstreamTerminalObserved,
		DownstreamDeliveryOutcome: src.DownstreamDeliveryOutcome,
		DownstreamBytesWritten:    src.DownstreamBytesWritten,
		DownstreamFlushError:      src.DownstreamFlushError,
		Truncated:                 src.Truncated,
		OmittedContacts:           src.OmittedContacts,
		OmittedLegs:               src.OmittedLegs,
		IncompleteReason:          src.IncompleteReason,
	}
	if src.FirstClientWriteOffsetMs != nil {
		v := *src.FirstClientWriteOffsetMs
		dst.FirstClientWriteOffsetMs = &v
	}
	if src.UpstreamTerminalOffsetMs != nil {
		v := *src.UpstreamTerminalOffsetMs
		dst.UpstreamTerminalOffsetMs = &v
	}
	if src.DownstreamDeliveryDoneOffsetMs != nil {
		v := *src.DownstreamDeliveryDoneOffsetMs
		dst.DownstreamDeliveryDoneOffsetMs = &v
	}
	if len(src.Legs) > 0 {
		dst.Legs = make([]RequestFlowLeg, len(src.Legs))
		copy(dst.Legs, src.Legs)
	}
	if len(src.Contacts) > 0 {
		dst.Contacts = make([]RequestFlowContact, len(src.Contacts))
		for i, c := range src.Contacts {
			dst.Contacts[i] = cloneFlowContact(c)
		}
	}
	return dst
}

func cloneFlowContact(c RequestFlowContact) RequestFlowContact {
	out := c
	out.ConnReadyOffsetMs = copyInt64Ptr(c.ConnReadyOffsetMs)
	out.WriteStartOffsetMs = copyInt64Ptr(c.WriteStartOffsetMs)
	out.WriteDoneOffsetMs = copyInt64Ptr(c.WriteDoneOffsetMs)
	out.ResponseHeadersOffsetMs = copyInt64Ptr(c.ResponseHeadersOffsetMs)
	out.FirstUpstreamByteOffsetMs = copyInt64Ptr(c.FirstUpstreamByteOffsetMs)
	out.EndOffsetMs = copyInt64Ptr(c.EndOffsetMs)
	out.HTTPStatusCode = copyIntPtr(c.HTTPStatusCode)
	return out
}

func copyInt64Ptr(src *int64) *int64 {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func copyIntPtr(src *int) *int {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

// normalizeFlowTarget 清洗目标快照字段长度和空白。
func normalizeFlowTarget(t RequestFlowTargetSnapshot) RequestFlowTargetSnapshot {
	return RequestFlowTargetSnapshot{
		UpstreamType:       clampFlowText(t.UpstreamType, 32),
		UpstreamSourceName: clampFlowText(t.UpstreamSourceName, 160),
		UpstreamName:       clampFlowText(t.UpstreamName, 160),
		UpstreamID:         t.UpstreamID,
		EndpointName:       clampFlowText(t.EndpointName, 160),
		RouteMode:          clampFlowText(t.RouteMode, 64),
		UpstreamModel:      clampFlowText(t.UpstreamModel, 160),
	}
}

func sameFlowTarget(a, b RequestFlowTargetSnapshot) bool {
	return a.UpstreamType == b.UpstreamType &&
		a.UpstreamSourceName == b.UpstreamSourceName &&
		a.UpstreamName == b.UpstreamName &&
		a.UpstreamID == b.UpstreamID &&
		a.EndpointName == b.EndpointName &&
		a.RouteMode == b.RouteMode &&
		a.UpstreamModel == b.UpstreamModel
}

func clampFlowText(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	// 按 rune 安全截断避免破坏 UTF-8 多字节序列。
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

func normalizeFlowTransport(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case FlowTransportHTTP:
		return FlowTransportHTTP
	case FlowTransportWS:
		return FlowTransportWS
	default:
		return ""
	}
}

func normalizeFlowOrigin(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case FlowOriginClientRequest:
		return FlowOriginClientRequest
	case FlowOriginServerSuccessor:
		return FlowOriginServerSuccessor
	default:
		return ""
	}
}

func normalizeDeliveryOutcome(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case FlowDeliveryDelivered:
		return FlowDeliveryDelivered
	case FlowDeliveryPartialOrFailed:
		return FlowDeliveryPartialOrFailed
	case FlowDeliveryAborted:
		return FlowDeliveryAborted
	default:
		return ""
	}
}

func clampNonNegOffset(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func resolveFlowEventOffset(startTime time.Time, evt RequestFlowEvent, fallback time.Time) int64 {
	if evt.OffsetMs != nil {
		return clampNonNegOffset(*evt.OffsetMs)
	}
	t := fallback
	if evt.At != nil && !evt.At.IsZero() {
		t = *evt.At
	}
	if startTime.IsZero() || t.IsZero() {
		return 0
	}
	return clampNonNegOffset(t.Sub(startTime).Milliseconds())
}

// ApplyRequestFlowEvents 将一组离散路径事件按统一口径归并到 RequestFlow。
// 返回值表示持久化可见内容是否发生变化。
func ApplyRequestFlowEvents(flowPtr **RequestFlow, startTime time.Time, fallbackNow time.Time, events ...RequestFlowEvent) bool {
	if flowPtr == nil || len(events) == 0 {
		return false
	}
	if *flowPtr == nil {
		*flowPtr = &RequestFlow{
			SchemaVersion: RequestFlowSchemaVersion,
			Transport:     FlowTransportHTTP,
			Origin:        FlowOriginClientRequest,
		}
	}
	flow := *flowPtr
	if flow.SchemaVersion <= 0 {
		flow.SchemaVersion = RequestFlowSchemaVersion
	}

	changed := false
	for _, evt := range events {
		switch evt.Kind {
		case FlowEventInit:
			if t := normalizeFlowTransport(evt.Transport); t != "" && flow.Transport != t {
				flow.Transport = t
				changed = true
			}
			if o := normalizeFlowOrigin(evt.Origin); o != "" && flow.Origin != o {
				flow.Origin = o
				changed = true
			}
		case FlowEventLegSelected:
			if applyLegSelectedEvent(flow, startTime, fallbackNow, evt) {
				changed = true
			}
		case FlowEventContactStart:
			if applyContactStartEvent(flow, startTime, fallbackNow, evt) {
				changed = true
			}
		case FlowEventContactConnReady:
			if c := findOrSelectContact(flow, evt.ContactSeq); c != nil && c.ConnReadyOffsetMs == nil {
				v := resolveFlowEventOffset(startTime, evt, fallbackNow)
				c.ConnReadyOffsetMs = &v
				changed = true
			}
		case FlowEventContactWriteStart:
			if c := findOrSelectContact(flow, evt.ContactSeq); c != nil && c.WriteStartOffsetMs == nil {
				v := resolveFlowEventOffset(startTime, evt, fallbackNow)
				c.WriteStartOffsetMs = &v
				changed = true
			}
		case FlowEventContactWriteDone:
			if c := findOrSelectContact(flow, evt.ContactSeq); c != nil && c.WriteDoneOffsetMs == nil {
				v := resolveFlowEventOffset(startTime, evt, fallbackNow)
				c.WriteDoneOffsetMs = &v
				changed = true
			}
		case FlowEventContactResponseHeaders:
			if c := findOrSelectContact(flow, evt.ContactSeq); c != nil {
				updated := false
				if c.ResponseHeadersOffsetMs == nil {
					v := resolveFlowEventOffset(startTime, evt, fallbackNow)
					c.ResponseHeadersOffsetMs = &v
					updated = true
				}
				if evt.HTTPStatusCode != nil && c.HTTPStatusCode == nil {
					code := *evt.HTTPStatusCode
					c.HTTPStatusCode = &code
					updated = true
				}
				if updated {
					changed = true
				}
			}
		case FlowEventContactFirstByte:
			if c := findOrSelectContact(flow, evt.ContactSeq); c != nil && c.FirstUpstreamByteOffsetMs == nil {
				v := resolveFlowEventOffset(startTime, evt, fallbackNow)
				c.FirstUpstreamByteOffsetMs = &v
				changed = true
			}
		case FlowEventContactEnd:
			if c := findOrSelectContact(flow, evt.ContactSeq); c != nil {
				updated := false
				if c.EndOffsetMs == nil {
					v := resolveFlowEventOffset(startTime, evt, fallbackNow)
					c.EndOffsetMs = &v
					updated = true
				}
				if evt.OutcomeCode != "" && c.OutcomeCode != evt.OutcomeCode {
					c.OutcomeCode = clampFlowText(evt.OutcomeCode, 64)
					updated = true
				}
				if evt.HTTPStatusCode != nil && (c.HTTPStatusCode == nil || *c.HTTPStatusCode != *evt.HTTPStatusCode) {
					code := *evt.HTTPStatusCode
					c.HTTPStatusCode = &code
					updated = true
				}
				if updated {
					changed = true
				}
			}
		case FlowEventDownstreamFirstWrite:
			if flow.FirstClientWriteOffsetMs == nil {
				v := resolveFlowEventOffset(startTime, evt, fallbackNow)
				flow.FirstClientWriteOffsetMs = &v
				changed = true
			}
			if evt.Bytes > flow.DownstreamBytesWritten {
				flow.DownstreamBytesWritten = evt.Bytes
				changed = true
			}
		case FlowEventDownstreamWrite:
			if evt.Bytes > 0 {
				if flow.FirstClientWriteOffsetMs == nil {
					v := resolveFlowEventOffset(startTime, evt, fallbackNow)
					flow.FirstClientWriteOffsetMs = &v
				}
				flow.DownstreamBytesWritten += evt.Bytes
				changed = true
			}
		case FlowEventUpstreamTerminal:
			if !flow.UpstreamTerminalObserved {
				v := resolveFlowEventOffset(startTime, evt, fallbackNow)
				flow.UpstreamTerminalObserved = true
				flow.UpstreamTerminalOffsetMs = &v
				changed = true
			}
		case FlowEventDownstreamDone:
			updated := false
			// 完成事件携带写出总数，兼容已记录首包及旧的增量事件。
			if evt.Bytes > flow.DownstreamBytesWritten {
				flow.DownstreamBytesWritten = evt.Bytes
				updated = true
			}
			if flow.DownstreamDeliveryDoneOffsetMs == nil {
				v := resolveFlowEventOffset(startTime, evt, fallbackNow)
				flow.DownstreamDeliveryDoneOffsetMs = &v
				updated = true
			}
			if outcome := normalizeDeliveryOutcome(evt.DeliveryOutcome); outcome != "" && flow.DownstreamDeliveryOutcome != outcome {
				flow.DownstreamDeliveryOutcome = outcome
				updated = true
			}
			if evt.FlushError && !flow.DownstreamFlushError {
				flow.DownstreamFlushError = true
				updated = true
			}
			if updated {
				changed = true
			}
		case FlowEventMarkIncomplete:
			reason := clampFlowText(evt.IncompleteReason, 64)
			if reason != "" && flow.IncompleteReason != reason {
				if flow.IncompleteReason == "" {
					flow.IncompleteReason = reason
				} else if !strings.Contains(flow.IncompleteReason, reason) {
					flow.IncompleteReason = clampFlowText(flow.IncompleteReason+","+reason, 128)
				}
				changed = true
			}
		}
	}

	if changed {
		compactFlowLocked(flow, MaxFlowContacts, MaxFlowLegs)
	}
	return changed
}

func applyLegSelectedEvent(flow *RequestFlow, startTime, fallbackNow time.Time, evt RequestFlowEvent) bool {
	target := RequestFlowTargetSnapshot{}
	if evt.Target != nil {
		target = normalizeFlowTarget(*evt.Target)
	}
	reason := clampFlowText(evt.ReasonCode, 64)
	offsetMs := resolveFlowEventOffset(startTime, evt, fallbackNow)

	if evt.LegSeq > 0 {
		for i := range flow.Legs {
			if flow.Legs[i].LegSeq == evt.LegSeq {
				updated := false
				if !sameFlowTarget(flow.Legs[i].TargetSnapshot, target) && (target.UpstreamName != "" || target.UpstreamType != "" || target.EndpointName != "") {
					flow.Legs[i].TargetSnapshot = target
					updated = true
				}
				if reason != "" && flow.Legs[i].ReasonCode == "" {
					flow.Legs[i].ReasonCode = reason
					updated = true
				}
				return updated
			}
		}
		if evt.LegSeq > flow.TotalLegs {
			flow.TotalLegs = evt.LegSeq
		}
		flow.Legs = append(flow.Legs, RequestFlowLeg{
			LegSeq:           evt.LegSeq,
			TargetSnapshot:   target,
			SelectedOffsetMs: offsetMs,
			ReasonCode:       reason,
		})
		return true
	}

	if !evt.ForceNewLeg && len(flow.Legs) > 0 {
		last := &flow.Legs[len(flow.Legs)-1]
		if sameFlowTarget(last.TargetSnapshot, target) {
			if reason != "" && last.ReasonCode == "" {
				last.ReasonCode = reason
				return true
			}
			return false
		}
	}

	flow.TotalLegs++
	if flow.TotalLegs < len(flow.Legs)+1 {
		flow.TotalLegs = len(flow.Legs) + 1
	}
	flow.Legs = append(flow.Legs, RequestFlowLeg{
		LegSeq:           flow.TotalLegs,
		TargetSnapshot:   target,
		SelectedOffsetMs: offsetMs,
		ReasonCode:       reason,
	})
	return true
}

func applyContactStartEvent(flow *RequestFlow, startTime, fallbackNow time.Time, evt RequestFlowEvent) bool {
	offsetMs := resolveFlowEventOffset(startTime, evt, fallbackNow)
	legSeq := evt.LegSeq
	if legSeq <= 0 && len(flow.Legs) > 0 {
		legSeq = flow.Legs[len(flow.Legs)-1].LegSeq
	}

	if evt.ContactSeq > 0 {
		for i := range flow.Contacts {
			if flow.Contacts[i].ContactSeq == evt.ContactSeq {
				return false
			}
		}
		if evt.ContactSeq > flow.TotalContacts {
			flow.TotalContacts = evt.ContactSeq
		}
		flow.Contacts = append(flow.Contacts, RequestFlowContact{
			ContactSeq:      evt.ContactSeq,
			LegSeq:          legSeq,
			StartOffsetMs:   offsetMs,
			OutcomeCode:     clampFlowText(evt.OutcomeCode, 64),
			TransportResend: evt.TransportResend,
		})
		return true
	}

	flow.TotalContacts++
	if flow.TotalContacts < len(flow.Contacts)+1 {
		flow.TotalContacts = len(flow.Contacts) + 1
	}
	flow.Contacts = append(flow.Contacts, RequestFlowContact{
		ContactSeq:      flow.TotalContacts,
		LegSeq:          legSeq,
		StartOffsetMs:   offsetMs,
		OutcomeCode:     clampFlowText(evt.OutcomeCode, 64),
		TransportResend: evt.TransportResend,
	})
	return true
}

func findOrSelectContact(flow *RequestFlow, contactSeq int) *RequestFlowContact {
	if len(flow.Contacts) == 0 {
		return nil
	}
	if contactSeq <= 0 {
		return &flow.Contacts[len(flow.Contacts)-1]
	}
	for i := len(flow.Contacts) - 1; i >= 0; i-- {
		if flow.Contacts[i].ContactSeq == contactSeq {
			return &flow.Contacts[i]
		}
	}
	return nil
}

// compactFlowLocked 保证 Contacts 和 Legs 遵守数量上限：
// 保留首次 2 个、所有切换边界 contact、以及最近 8 个 contact，及其引用的 leg。
func compactFlowLocked(flow *RequestFlow, maxContacts, maxLegs int) {
	if flow == nil {
		return
	}
	if flow.TotalContacts < len(flow.Contacts)+flow.OmittedContacts {
		flow.TotalContacts = len(flow.Contacts) + flow.OmittedContacts
	}
	if flow.TotalLegs < len(flow.Legs)+flow.OmittedLegs {
		flow.TotalLegs = len(flow.Legs) + flow.OmittedLegs
	}

	if maxContacts > 0 && len(flow.Contacts) > maxContacts {
		selected := selectBoundedContacts(flow.Contacts, maxContacts)
		flow.Contacts = selected
		flow.Truncated = true
		flow.OmittedContacts = flow.TotalContacts - len(flow.Contacts)
		if flow.OmittedContacts < 0 {
			flow.OmittedContacts = 0
		}
	}

	if maxLegs > 0 && len(flow.Legs) > maxLegs {
		referenced := make(map[int]struct{}, len(flow.Contacts)+2)
		for _, c := range flow.Contacts {
			if c.LegSeq > 0 {
				referenced[c.LegSeq] = struct{}{}
			}
		}
		referenced[flow.Legs[0].LegSeq] = struct{}{}
		referenced[flow.Legs[len(flow.Legs)-1].LegSeq] = struct{}{}
		// 字节预算至少容纳首尾段和保留下来的 contact 所属段。
		kept := selectBoundedLegs(flow.Legs, referenced, max(maxLegs, len(referenced)))
		flow.Legs = kept
		flow.Truncated = true
		flow.OmittedLegs = flow.TotalLegs - len(flow.Legs)
		if flow.OmittedLegs < 0 {
			flow.OmittedLegs = 0
		}
	}
}

func selectBoundedContacts(contacts []RequestFlowContact, budget int) []RequestFlowContact {
	n := len(contacts)
	if n <= budget {
		return contacts
	}
	keep := make([]bool, n)
	keptCount := 0
	mark := func(idx int) {
		if idx >= 0 && idx < n && !keep[idx] && keptCount < budget {
			keep[idx] = true
			keptCount++
		}
	}

	// 字节预算收紧到两条时也要保留真实首尾。
	mark(0)
	mark(n - 1)
	mark(1)
	for i := n - 2; i >= 0 && i >= n-8; i-- {
		mark(i)
	}

	// 2. 保留所有切换 leg 的边界 contact。
	for i := 1; i < n; i++ {
		if contacts[i].LegSeq != contacts[i-1].LegSeq {
			mark(i)
		}
	}

	// 3. 若仍有余量，优先补齐最近尾部 contact。
	for i := n - 1; i >= 0 && keptCount < budget; i-- {
		mark(i)
	}

	out := make([]RequestFlowContact, 0, keptCount)
	for i := 0; i < n; i++ {
		if keep[i] {
			out = append(out, contacts[i])
		}
	}
	return out
}

func selectBoundedLegs(legs []RequestFlowLeg, referenced map[int]struct{}, budget int) []RequestFlowLeg {
	n := len(legs)
	if n <= budget {
		return legs
	}
	keep := make([]bool, n)
	keptCount := 0
	mark := func(idx int) {
		if idx >= 0 && idx < n && !keep[idx] && keptCount < budget {
			keep[idx] = true
			keptCount++
		}
	}

	// 优先保留所有已选择 contact 的目标，避免留下悬空引用。
	for i := range legs {
		if _, ok := referenced[legs[i].LegSeq]; ok {
			mark(i)
		}
	}
	for i := n - 1; i >= 0 && keptCount < budget; i-- {
		mark(i)
	}

	out := make([]RequestFlowLeg, 0, keptCount)
	for i := 0; i < n; i++ {
		if keep[i] {
			out = append(out, legs[i])
		}
	}
	return out
}

// MarshalRequestFlowBounded 将 RequestFlow 编码为不超过 32 KiB 的 JSON。
func MarshalRequestFlowBounded(flow *RequestFlow) (string, error) {
	if flow == nil {
		return "", nil
	}
	copyFlow := CloneRequestFlow(flow)
	if copyFlow.SchemaVersion <= 0 {
		copyFlow.SchemaVersion = RequestFlowSchemaVersion
	}
	compactFlowLocked(copyFlow, MaxFlowContacts, MaxFlowLegs)

	raw, err := json.Marshal(copyFlow)
	if err != nil {
		return "", err
	}
	if len(raw) <= MaxFlowJSONBytes {
		return string(raw), nil
	}

	// 字节预算仍超限时逐步收紧接触点与 leg 预算，直到满足 32 KiB 限制。
	for len(raw) > MaxFlowJSONBytes && (len(copyFlow.Contacts) > 2 || len(copyFlow.Legs) > 2) {
		// 先移除没有保留联系记录的选路。剩余选路都被引用时，再一起收紧两种预算。
		legs := len(copyFlow.Legs)
		legBudget := max(2, legs/2)
		compactFlowLocked(copyFlow, len(copyFlow.Contacts), legBudget)
		if len(copyFlow.Legs) == legs {
			if len(copyFlow.Contacts) <= 2 {
				// 剩余联系和选路都必须保留，转为裁切字段。
				break
			}
			compactFlowLocked(copyFlow, max(2, len(copyFlow.Contacts)/2), legBudget)
		}
		copyFlow.Truncated = true
		raw, err = json.Marshal(copyFlow)
		if err != nil {
			return "", err
		}
	}
	if len(raw) > MaxFlowJSONBytes {
		// 保留首尾和引用关系，限制剩余文本字段。
		copyFlow.Transport = normalizeFlowTransport(copyFlow.Transport)
		copyFlow.Origin = normalizeFlowOrigin(copyFlow.Origin)
		copyFlow.DownstreamDeliveryOutcome = normalizeDeliveryOutcome(copyFlow.DownstreamDeliveryOutcome)
		copyFlow.IncompleteReason = clampFlowText(copyFlow.IncompleteReason, 128)
		for i := range copyFlow.Legs {
			copyFlow.Legs[i].ReasonCode = clampFlowText(copyFlow.Legs[i].ReasonCode, 32)
			copyFlow.Legs[i].TargetSnapshot = normalizeFlowTarget(copyFlow.Legs[i].TargetSnapshot)
		}
		for i := range copyFlow.Contacts {
			copyFlow.Contacts[i].OutcomeCode = clampFlowText(copyFlow.Contacts[i].OutcomeCode, 32)
		}
		copyFlow.Truncated = true
		raw, err = json.Marshal(copyFlow)
		if err != nil {
			return "", err
		}
	}
	return string(raw), nil
}

// ParseRequestFlowWhitelist 严格按白名单解码 request_flow_json，明确区分旧数据缺失、损坏 JSON 与不支持版本。
func ParseRequestFlowWhitelist(raw string, hasRow bool) (*RequestFlow, RequestIntegrity) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if hasRow {
			return nil, RequestIntegrity{
				RecordMissingFlow: true,
				DiagnosticCode:    "legacy_unrecorded",
				DiagnosticMessage: "未记录逐次路径",
			}
		}
		return nil, RequestIntegrity{}
	}

	var decoded RequestFlow
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, RequestIntegrity{
			CorruptFlowJSON:   true,
			DiagnosticCode:    "corrupt_flow_json",
			DiagnosticMessage: "路径记录不可读",
		}
	}
	if decoded.SchemaVersion <= 0 || decoded.SchemaVersion > RequestFlowSchemaVersion {
		return nil, RequestIntegrity{
			UnsupportedSchema: true,
			DiagnosticCode:    "unsupported_flow_schema",
			DiagnosticMessage: "路径记录不可读",
		}
	}

	clean := &RequestFlow{
		SchemaVersion:                  decoded.SchemaVersion,
		Transport:                      normalizeFlowTransport(decoded.Transport),
		Origin:                         normalizeFlowOrigin(decoded.Origin),
		TotalLegs:                      decoded.TotalLegs,
		TotalContacts:                  decoded.TotalContacts,
		FirstClientWriteOffsetMs:       sanitizeOffsetPtr(decoded.FirstClientWriteOffsetMs),
		UpstreamTerminalOffsetMs:       sanitizeOffsetPtr(decoded.UpstreamTerminalOffsetMs),
		UpstreamTerminalObserved:       decoded.UpstreamTerminalObserved,
		DownstreamDeliveryDoneOffsetMs: sanitizeOffsetPtr(decoded.DownstreamDeliveryDoneOffsetMs),
		DownstreamDeliveryOutcome:      normalizeDeliveryOutcome(decoded.DownstreamDeliveryOutcome),
		DownstreamBytesWritten:         clampNonNegOffset(decoded.DownstreamBytesWritten),
		DownstreamFlushError:           decoded.DownstreamFlushError,
		Truncated:                      decoded.Truncated,
		OmittedContacts:                maxIntZero(decoded.OmittedContacts),
		OmittedLegs:                    maxIntZero(decoded.OmittedLegs),
		IncompleteReason:               clampFlowText(decoded.IncompleteReason, 128),
	}

	legLimit := len(decoded.Legs)
	if legLimit > MaxFlowLegs {
		legLimit = MaxFlowLegs
		clean.Truncated = true
	}
	if legLimit > 0 {
		clean.Legs = make([]RequestFlowLeg, 0, legLimit)
		for i := 0; i < legLimit; i++ {
			leg := decoded.Legs[i]
			clean.Legs = append(clean.Legs, RequestFlowLeg{
				LegSeq:           maxIntZero(leg.LegSeq),
				TargetSnapshot:   normalizeFlowTarget(leg.TargetSnapshot),
				SelectedOffsetMs: clampNonNegOffset(leg.SelectedOffsetMs),
				ReasonCode:       clampFlowText(leg.ReasonCode, 64),
			})
		}
	}

	contactLimit := len(decoded.Contacts)
	if contactLimit > MaxFlowContacts {
		contactLimit = MaxFlowContacts
		clean.Truncated = true
	}
	if contactLimit > 0 {
		clean.Contacts = make([]RequestFlowContact, 0, contactLimit)
		for i := 0; i < contactLimit; i++ {
			c := decoded.Contacts[i]
			clean.Contacts = append(clean.Contacts, RequestFlowContact{
				ContactSeq:                maxIntZero(c.ContactSeq),
				LegSeq:                    maxIntZero(c.LegSeq),
				StartOffsetMs:             clampNonNegOffset(c.StartOffsetMs),
				ConnReadyOffsetMs:         sanitizeOffsetPtr(c.ConnReadyOffsetMs),
				WriteStartOffsetMs:        sanitizeOffsetPtr(c.WriteStartOffsetMs),
				WriteDoneOffsetMs:         sanitizeOffsetPtr(c.WriteDoneOffsetMs),
				ResponseHeadersOffsetMs:   sanitizeOffsetPtr(c.ResponseHeadersOffsetMs),
				FirstUpstreamByteOffsetMs: sanitizeOffsetPtr(c.FirstUpstreamByteOffsetMs),
				EndOffsetMs:               sanitizeOffsetPtr(c.EndOffsetMs),
				HTTPStatusCode:            copyIntPtr(c.HTTPStatusCode),
				OutcomeCode:               clampFlowText(c.OutcomeCode, 64),
				TransportResend:           c.TransportResend,
			})
		}
	}

	integrity := RequestIntegrity{
		Truncated:        clean.Truncated,
		OmittedContacts:  clean.OmittedContacts,
		OmittedLegs:      clean.OmittedLegs,
		IncompleteReason: clean.IncompleteReason,
	}
	if clean.IncompleteReason != "" {
		integrity.WriteGap = true
		integrity.WriteGapCode = clean.IncompleteReason
		integrity.DiagnosticCode = clean.IncompleteReason
		integrity.DiagnosticMessage = "追踪记录存在写入缺口"
	} else if clean.Truncated {
		integrity.DiagnosticCode = "flow_truncated"
		integrity.DiagnosticMessage = "联系次数超过上限，已按首尾边界折叠部分中间尝试"
	}

	return clean, integrity
}

func sanitizeOffsetPtr(ptr *int64) *int64 {
	if ptr == nil {
		return nil
	}
	v := clampNonNegOffset(*ptr)
	return &v
}

func maxIntZero(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
