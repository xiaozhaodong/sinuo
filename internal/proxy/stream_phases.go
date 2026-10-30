package proxy

import (
	"strconv"
	"strings"
	"time"

	"sinuo/internal/tracking"
)

const (
	streamPhaseReadLimit = 8 << 20
	streamPhaseMarkLimit = 1 << 16
)

type streamFlowMark struct {
	at  int64
	sum int64
}

// StreamPhaseCollector 由调用方计时锁保护；只存时间和累计字节，不保存内容。
type StreamPhaseCollector struct {
	start                  time.Time
	first, text, reasoning *int64
	hidden                 bool
	thinkingBlock          bool
	read                   int64
	sum                    int64
	flow                   []streamFlowMark
	overflow               bool
	stopReason             string
	reasoningTokens        int64
}

func newStreamPhaseCollector(start time.Time) *StreamPhaseCollector {
	return &StreamPhaseCollector{start: start}
}

// Observe 的 eventBytes 为本次新增读取字节；内容权重只取 result.ContentBytes。
// 返回首次内容的绝对时刻，供调用方发布一次性实时事件。
func (c *StreamPhaseCollector) Observe(result *ParseResult, eventBytes int, at time.Time) *time.Time {
	c.read += int64(eventBytes)
	if c.read > streamPhaseReadLimit {
		c.overflow, c.flow = true, nil
	}
	if result == nil {
		return nil
	}
	c.hidden = c.hidden || result.ReasoningHidden
	c.thinkingBlock = c.thinkingBlock || result.ThinkingBlock
	if result.ResponseReasoningFields.ReasoningTokens != nil {
		c.reasoningTokens = *result.ResponseReasoningFields.ReasoningTokens
	}
	if tokens := streamReasoningTokens(result.ResponseReasoning); tokens > 0 {
		c.reasoningTokens = tokens
	}
	if result.StopReason != "" {
		c.stopReason = result.StopReason
	}
	if result.ContentKind == ContentNone {
		return nil
	}
	ms := max(at.Sub(c.start).Milliseconds(), 0)
	var firstAt *time.Time
	if c.first == nil {
		c.first, firstAt = &ms, &at
	}
	if result.ContentKind == ContentText && c.text == nil {
		c.text = &ms
	}
	if result.ContentKind == ContentReasoning && c.reasoning == nil {
		c.reasoning = &ms
	}
	c.mark(ms, result.ContentBytes)
	return firstAt
}

func (c *StreamPhaseCollector) mark(ms int64, size int) {
	if c.overflow || size <= 0 {
		return
	}
	c.sum += int64(size)
	k := len(c.flow)
	if k > 0 && c.flow[k-1].at == ms {
		c.flow[k-1].sum = c.sum
		return
	}
	if k >= streamPhaseMarkLimit {
		c.overflow, c.flow = true, nil
		return
	}
	c.flow = append(c.flow, streamFlowMark{ms, c.sum})
}

// Snapshot 在最终快照中落定流速与不可见思考标记，并复制指针避免跨层共享。
func (c *StreamPhaseCollector) Snapshot(reasoningTokens int64) tracking.StreamPhases {
	if reasoningTokens <= 0 {
		reasoningTokens = c.reasoningTokens
	}
	hidden := c.hidden || (c.thinkingBlock && c.reasoning == nil)
	p := tracking.StreamPhases{
		FirstContentMs: c.first, FirstTextMs: c.text, FirstReasoningMs: c.reasoning,
		ReasoningHidden: &hidden,
	}
	if c.stopReason != "" {
		p.StopReason = &c.stopReason
	}
	from := c.first
	if reasoningTokens > 0 && c.text != nil {
		from = c.text
	}
	if from != nil {
		p.StreamFlowMs = c.flowMs(*from)
	}
	return p.Clone()
}

// flowMs 照 magpie：累计字节的10%与90%采样点，整数乘10/8，至少1ms。
func (c *StreamPhaseCollector) flowMs(from int64) *int64 {
	if c.overflow || len(c.flow) == 0 {
		return nil
	}
	var base int64
	for _, mark := range c.flow {
		if mark.at >= from {
			break
		}
		base = mark.sum
	}
	total := c.sum - base
	if total <= 0 {
		return nil
	}
	at := func(share int64) int64 {
		for _, mark := range c.flow {
			if mark.at >= from && (mark.sum-base)*100 >= total*share {
				return mark.at
			}
		}
		return c.flow[len(c.flow)-1].at
	}
	ms := max((at(90)-at(10))*10/8, 1)
	return &ms
}

func streamReasoningTokens(value string) int64 {
	for _, field := range strings.Fields(value) {
		if raw, ok := strings.CutPrefix(field, "reasoning_tokens:"); ok {
			count, err := strconv.ParseInt(raw, 10, 64)
			if err == nil && count > 0 {
				return count
			}
		}
	}
	return 0
}
