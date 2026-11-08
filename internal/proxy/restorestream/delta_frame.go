package restorestream

import (
	"bytes"
	"sync"
)

type argumentEdit struct {
	start, end int
	value      []byte
}
type deltaFrame struct {
	route                   object
	hint                    DeltaEvent
	nonempty, hasCR         bool
	partial                 *field
	dataLine                int // 单个 LF 事件中 data 行起点，负数表示其他布局
	parent                  *Writer
	key                     fieldKey
	sourceSlab, scratchSlab *workSlab

	codex          bool
	active, closed bool
	completeSource bool
	prefix, suffix []byte
	scratch        []byte
	argsBytes      int

	// Codex done 与 completed 快照要求与分片一致；缓存原始参数和已还原参数。
	source      []byte
	edits       []argumentEdit
	outputBytes int
}

func (w *deltaFrame) endValue(ev []byte) int {
	if !w.codex {
		return len(ev) - len(w.suffix)
	}
	if !bytes.HasSuffix(ev, []byte("}\n\n")) {
		return -1
	}
	i := len(ev) - 3
	end := i
	for i > 0 && ev[i-1] >= '0' && ev[i-1] <= '9' {
		i--
	}
	if i == end || end-i > 9 || end-i > 1 && ev[i] == '0' {
		return -1
	}
	return i - len(w.suffix)
}

type workSlab [16384]byte

var slabPool = sync.Pool{New: func() any { return new(workSlab) }}

func lend() (*workSlab, []byte) { b := slabPool.Get().(*workSlab); return b, b[:0] }
func (w *deltaFrame) release() {
	for _, b := range []*workSlab{w.scratchSlab, w.sourceSlab} {
		if b != nil {
			clear(b[:])
			slabPool.Put(b)
		}
	}

	w.scratchSlab = nil
	w.sourceSlab = nil

	w.scratch = nil
	w.source = nil
}
