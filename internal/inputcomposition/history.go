package inputcomposition

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// History 是同一条连接内某个响应的不可变快照。仅保留分类、估算量、可见轮次以及有界的工具关联。
type History struct {
	items   []Item
	turn    int
	calls   map[string]label
	reasons []string
}

// AnalyzeCreate 由代理按本连接的 previous_response_id 提供父快照。
func AnalyzeCreate(body []byte, parent *History) (*Summary, *History) {
	return analyze("/v1/responses", body, parent, true)
}

func containsHistoryBoundary(input gjson.Result) bool {
	found := false
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			kind := item.Get("type").Str
			found = kind == "compaction" || kind == "compaction_summary" || kind == "item_reference"
			return !found
		})
	}
	return found
}

// history 合并可继承的消息，不保存顶层配置或正文。先收当前项再收父项，分支之间不共享可变 map。
func (c *collector) history(previous []Item) *History {
	h := &History{turn: c.turn}
	if len(previous) == 0 {
		h.items = slices.DeleteFunc(c.items(), func(i Item) bool { return i.Group == "tools" })
	} else {
		merged := collector{weights: make(map[string]map[string]int)}
		for _, list := range [][]Item{c.items(), previous} {
			for _, i := range list {
				if i.Group == "tools" {
					continue
				}
				name := i.Name
				if i.Group == "chat" && isEarlierTurn(name, c.turn) {
					name = "较早对话"
				}
				merged.add(label{i.Group, name}, 4*i.Estimate)
			}
		}
		h.items = merged.items()
	}
	for _, reason := range c.summary.PartialReasons {
		switch reason {
		case "history_reference", "ws_create":
		default:
			h.reasons = append(h.reasons, reason)
		}
	}
	// 过多的并行调用放弃跨帧配对，下一帧仍可归入通用工具输出。
	if n := len(c.calls); n > 0 && n <= 32 {
		h.calls = make(map[string]label, n)
		for id, l := range c.calls {
			h.calls[strings.Clone(id)] = label{l.group, strings.Clone(l.name)}
		}
	}
	return h
}

// Completed 只在成功终态调用。上游输出此时才成为下一次续接的历史。
func (h *History) Completed(frame []byte, inputLimit int64, done map[int]*History) *History {
	if h == nil || len(frame) > maxBodyBytes {
		return nil
	}
	if inputLimit == 0 && !gjson.GetBytes(frame, "usage.input_tokens").Exists() &&
		!gjson.GetBytes(frame, "response.usage.input_tokens").Exists() {
		inputLimit = -1
	}
	status := gjson.GetBytes(frame, "response.status").Str
	if status != "" && status != "completed" {
		return nil
	}
	output := gjson.GetBytes(frame, "response.output")
	if containsHistoryBoundary(output) || gjson.GetBytes(frame, "response.truncation").Str == "auto" {
		return nil
	}
	s := &Summary{PartialReasons: slices.Clone(h.reasons)}
	c := collector{summary: s, weights: make(map[string]map[string]int), calls: maps.Clone(h.calls), turn: h.turn}
	if c.calls == nil {
		c.calls = make(map[string]label)
	}
	if output.Get("0").Exists() {
		output.ForEach(func(_, item gjson.Result) bool { c.message(item); return true })
	} else {
		// 部分 WS 上游只在 output_item.done 给出完整条目，终态只有 usage。
		for idx := 0; idx < maxOutputItems && len(done) > 0; idx++ {
			part := done[idx]
			if part == nil {
				continue
			}
			for _, reason := range part.reasons {
				if reason == "compaction" {
					return nil
				}
				s.notePartial(reason)
			}
			for _, i := range part.items {
				c.add(label{i.Group, i.Name}, i.Estimate*4)
			}
			for id, l := range part.calls {
				if len(c.calls) < maxCalls {
					c.calls[id] = l
				}
			}
		}
	}
	return c.history(limitItems(h.items, inputLimit))
}

const maxOutputItems = 32

// OutputItem 处理已完成的输出条目，序号在同一响应内去重。增量文本不参与采集。
func (h *History) OutputItem(frame []byte) (int, *History) {
	if h == nil || len(frame) > maxBodyBytes {
		return 0, nil
	}
	index := gjson.GetBytes(frame, "output_index")
	idx, err := strconv.Atoi(index.Raw)
	if err != nil || idx < 0 || idx >= maxOutputItems {
		return 0, nil
	}
	item := gjson.GetBytes(frame, "item")
	if !item.IsObject() {
		return 0, nil
	}
	c := collector{summary: &Summary{}, weights: make(map[string]map[string]int), calls: make(map[string]label), turn: h.turn}
	c.message(item)
	return idx, c.history(nil)
}

// 若父请求已上报更小的输入量，传给后代的历史估算也不能超过它。
func limitItems(items []Item, limit int64) []Item {
	if limit < 0 {
		return items
	}
	var sum int64
	for _, i := range items {
		sum += int64(i.Estimate)
	}
	if sum <= limit {
		return items
	}
	out := make([]Item, 0, len(items))
	var seen, allocated int64
	for _, i := range items {
		seen += int64(i.Estimate)
		next := int64(float64(seen) / float64(sum) * float64(limit))
		i.Estimate = int(next - allocated)
		allocated = next
		if i.Estimate > 0 {
			out = append(out, i)
		}
	}
	return out
}

const namedTurns = 12

func turnLabel(turn int) string { return "第 " + strconv.Itoa(turn) + " 轮对话" }

func isEarlierTurn(name string, latest int) bool {
	if latest <= namedTurns {
		return false
	}
	n, ok := strings.CutPrefix(name, "第 ")
	if !ok {
		return false
	}
	n, ok = strings.CutSuffix(n, " 轮对话")
	i, err := strconv.Atoi(n)
	return ok && err == nil && i <= latest-namedTurns
}

func (c *collector) addText(l label, text string) {
	if text == "" {
		return
	}
	if l.group == "chat" && (l.name == "用户消息" || l.name == "助手消息") {
		if c.newUser && l.name == "用户消息" {
			c.newUser = false
			c.turn++
			slot := &c.turnBytes[c.turn%namedTurns]
			c.add(label{"chat", "较早对话"}, *slot)
			*slot = 0
		}
		if c.turn > 0 {
			c.turnBytes[c.turn%namedTurns] += len(text)
			return
		}
	}
	c.add(l, len(text))
}
