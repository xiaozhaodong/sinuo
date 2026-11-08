package restorestream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestUntypedResponsesOpening(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		wire := strings.Join([]string{
			": keepalive", "", "id: 1", "event: response.created",
			`data: {"type":"response.created",`, `data: "response":{"output":[]}}`, "", "",
		}, newline)
		src := iotest.OneByteReader(strings.NewReader("\ufeff" + wire))
		replay, err := OpenUntypedResponses(context.Background(), bufio.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(replay)
		if err != nil || string(out) != wire {
			t.Fatal("首事件跨读取或行结束符被改变", err)
		}
	}
	for _, bad := range []string{
		"", ": keepalive\n\n", "data: {}\n", "data: [DONE]\n\n",
		"event: error\ndata: {\"type\":\"response.created\"}\n\n",
		"data: {\"type\":\"response.created\",\"type\":\"error\"}\n\n",
		": benign\n\n" + strings.Repeat("d", maxBytes),
	} {
		if _, err := OpenUntypedResponses(context.Background(), bufio.NewReader(strings.NewReader(bad))); !errors.Is(err, ErrResponseFormat) {
			t.Fatal("未拒绝未识别、截断或超限的首事件", err)
		}
	}
}

func TestUntypedResponsesStartsBeforeEOF(t *testing.T) {
	frame := []byte("data: {\"type\":\"response.created\",\"response\":{\"output\":[]}}\n\n")
	src := io.MultiReader(bytes.NewReader(frame), iotest.ErrReader(errors.New("不应预读后续事件")))
	replay, err := OpenUntypedResponses(context.Background(), bufio.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	actual := make([]byte, len(frame))
	if _, err := io.ReadFull(replay, actual); err != nil || !bytes.Equal(actual, frame) {
		t.Fatal("首事件没有立即交付", err)
	}
}

func TestUntypedResponsesLargeOpeningPreservesReadAhead(t *testing.T) {
	// 官方快照可达约 47 KB，末次预读还可能包含下一条 delta 的一部分。
	wire := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"instructions\":\"" +
		strings.Repeat("x", 47<<10) + "\"}}\n\ndata: {\"type\":\"response.in_progress\"}\n\n"
	replay, err := OpenUntypedResponses(context.Background(), bufio.NewReader(strings.NewReader(wire)))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(replay)
	if err != nil || string(actual) != wire {
		t.Fatal("跨缓冲的大首帧或后续事件被丢失或重复", err)
	}
}
