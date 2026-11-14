package response

// ClaudeBlockTail 识别输出上限截断最后一个内容块的情况，供流式交付和用量结算共用。
// open 为已开始但还未停止的块数。仅最后启动的块仍打开时，才允许原样交付截断终态。
type ClaudeBlockTail struct {
	open      int
	lastIndex int
	lastOpen  bool
	atLimit   bool
}

func (t *ClaudeBlockTail) Start(index int) {
	t.open++
	t.lastIndex, t.lastOpen = index, true
	t.atLimit = false
}

func (t *ClaudeBlockTail) Stop(index int) {
	t.open--
	if index == t.lastIndex {
		t.lastOpen = false
	}
	t.atLimit = false
}

// MessageDelta 允许用量更新单独成帧；只有明确的停止原因才能改变已识别的截断。
func (t *ClaudeBlockTail) MessageDelta(stopReason string) {
	if stopReason != "" {
		t.atLimit = stopReason == "max_tokens" && t.open == 1 && t.lastOpen
	}
}

func (t *ClaudeBlockTail) CutAtOutputLimit() bool { return t.atLimit }
