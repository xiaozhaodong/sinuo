package privacy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Claude Code 兼容性回归：官方 preserved thinking 校验要求历史 thinking、signature、
// 以及产生它们的 system/tools/messages 与原请求一致；脱敏只能动可扫描文本，且结果必须确定。

const claudeSecret = "sk-proj-abcdefghijklmnopqrstuvwxyz123456"

func TestApplyRedactLeavesThinkingBlocksUntouched(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	body := []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":"key ` + claudeSecret + `"},` +
		`{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"用户给了 ` + claudeSecret + `","signature":"sig-` + claudeSecret + `"},` +
		`{"type":"redacted_thinking","data":"enc-` + claudeSecret + `"},` +
		`{"type":"text","text":"收到 ` + claudeSecret + `"}]}]}`)

	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		t.Fatalf("redacted body invalid json: %v", err)
	}
	var blocks []map[string]string
	if err := json.Unmarshal(decoded.Messages[1].Content, &blocks); err != nil {
		t.Fatalf("decode assistant blocks: %v", err)
	}
	if blocks[0]["thinking"] != "用户给了 "+claudeSecret || blocks[0]["signature"] != "sig-"+claudeSecret {
		t.Fatalf("thinking 与 signature 不得被改动: %+v", blocks[0])
	}
	if blocks[1]["data"] != "enc-"+claudeSecret {
		t.Fatalf("redacted_thinking.data 不得被改动: %+v", blocks[1])
	}
	if blocks[2]["text"] != "收到 [OpenAI密钥]" {
		t.Fatalf("assistant 历史 text 仍按规则脱敏，got %q", blocks[2]["text"])
	}
	if !strings.Contains(string(decoded.Messages[0].Content), "[OpenAI密钥]") {
		t.Fatalf("user 文本应被脱敏: %s", decoded.Messages[0].Content)
	}
}

func TestApplyRedactIsDeterministicAcrossTurns(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	firstTurn := `{"role":"user","content":"key ` + claudeSecret + `"}`
	turn1 := []byte(`{"model":"claude-sonnet-5","messages":[` + firstTurn + `]}`)
	turn2 := []byte(`{"model":"claude-sonnet-5","messages":[` + firstTurn +
		`,{"role":"assistant","content":"ok"},{"role":"user","content":"再来一次 ` + claudeSecret + `"}]}`)

	first, err := snapshot.Apply(claudeRequest(), turn1)
	if err != nil {
		t.Fatalf("apply turn1 failed: %v", err)
	}
	again, err := snapshot.Apply(claudeRequest(), turn1)
	if err != nil {
		t.Fatalf("apply turn1 again failed: %v", err)
	}
	if !bytes.Equal(first.Body, again.Body) {
		t.Fatalf("同规则同输入必须得到同样的输出:\n%s\n%s", first.Body, again.Body)
	}

	second, err := snapshot.Apply(claudeRequest(), turn2)
	if err != nil {
		t.Fatalf("apply turn2 failed: %v", err)
	}
	// 历史消息脱敏结果逐字节一致，preserved thinking 校验才能通过
	redactedFirstTurn := `{"role":"user","content":"key [OpenAI密钥]"}`
	if !bytes.Contains(first.Body, []byte(redactedFirstTurn)) || !bytes.Contains(second.Body, []byte(redactedFirstTurn)) {
		t.Fatalf("两轮中的历史消息应得到相同的脱敏结果:\n%s\n%s", first.Body, second.Body)
	}
}

func TestApplyKeepsClaudeAttributionBlock(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	attribution := "x-anthropic-billing-header: cc_version=2.1.281; cch=" + claudeSecret
	body := []byte(`{"model":"claude-sonnet-5","system":[` +
		`{"type":"text","text":"` + attribution + `"},` +
		`{"type":"text","text":"system key ` + claudeSecret + `"}],` +
		`"messages":[{"role":"user","content":"` + attribution + `"}]}`)

	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	var decoded struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		t.Fatalf("redacted body invalid json: %v", err)
	}
	if decoded.System[0].Text != attribution {
		t.Fatalf("system 首块 attribution 必须原样保留，got %q", decoded.System[0].Text)
	}
	if decoded.System[1].Text != "system key [OpenAI密钥]" {
		t.Fatalf("其余 system 文本照常脱敏，got %q", decoded.System[1].Text)
	}
	if strings.Contains(decoded.Messages[0].Content, claudeSecret) {
		t.Fatalf("attribution 前缀只在 system 首块豁免，消息里照常脱敏，got %q", decoded.Messages[0].Content)
	}
}
