package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/store"

	"github.com/tidwall/gjson"
)

func TestPrivacyExactVariantsImportReloadAndRestore(t *testing.T) {
	const value = "A+/:?&= Z!"
	forms := []string{value, "A%2B%2F%3A%3F%26%3D+Z%21", "A%2b%2f%3a%3f%26%3d%20Z%21", "A+%2F:%3F&=%20Z%21", "A+%2F%3A%3F&=%20Z%21", "QSsvOj8mPSBaIQ==", "QSsvOj8mPSBaIQ"}
	text := strings.Join(forms, " | ")
	ctx := context.Background()
	for _, source := range []string{"manual", "config-url"} {
		t.Run(source, func(t *testing.T) {
			s := newTestPrivacyService(t)
			settings, err := s.GetSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.Mode, settings.ResponseRestoreEnabled = privacy.ModeRedact, true
			if _, err = s.UpdateSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			if source == "manual" {
				if _, err = s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Enabled: true, Name: "合成密码", SecretValue: value, Category: "password"}); err != nil {
					t.Fatal(err)
				}
			} else {
				// URL 提取出的解码密码应立即保护 URL 内的编码拼写，无需选中整条 URL。
				p := configPreview(t, s, `{"database_url":"postgresql://user:A+%2F%3A%3F&=%20Z%21@db.invalid/app"}`)
				if r, err := s.CommitConfigImport(ctx, p.ID, suggestedIDs(p)); err != nil || r.Added != 1 {
					t.Fatal("应仅导入解码密码", r, err)
				}
			}
			for _, reload := range []bool{false, true} {
				if reload {
					if err := s.Initialize(ctx); err != nil {
						t.Fatal(err)
					}
				}
				records, err := s.ListExactSecrets(ctx)
				if err != nil || len(records) != 1 || records[0].SecretValue != value {
					t.Fatal("变体不应单独入库或改变登记值", err)
				}
				for _, path := range []string{"/v1/messages", "/v1/responses"} {
					req := privacy.Request{Path: path, StreamingHTTP: true, UpstreamType: privacy.UpstreamTypeEndpoint}
					var input any = map[string]any{"stream": true, "messages": []map[string]string{{"role": "user", "content": text}}}
					if path == "/v1/responses" {
						req.UpstreamType = privacy.UpstreamTypeAccount
						args, _ := json.Marshal(map[string]string{"password": text})
						input = map[string]any{"stream": true, "input": []map[string]string{{"type": "function_call", "name": "tool", "call_id": "synthetic-call", "arguments": string(args)}}}
					}
					body, _ := json.Marshal(input)
					r, err := s.Apply(ctx, req, body)
					if err != nil || !r.Changed || r.Restore == nil || r.HitCount != len(forms) || len(r.RuleHits) != 1 {
						t.Fatal("原值与变体应命中同一规则且完整遮蔽", path, err, r.HitCount)
					}
					tokens := regexp.MustCompile(`\{\{SNU_[0-9a-f]{32}\}\}`).FindAllString(string(r.Body), -1)
					if len(tokens) != len(forms) {
						t.Fatal("未逐个替换编码形式", tokens)
					}
					for i, token := range tokens {
						if actual, ok := r.Restore.Lookup(token); !ok || actual != forms[i] {
							t.Fatal("回填映射丢失编码形式", i)
						}
					}
					if err := r.Restore.Publish(); err != nil {
						t.Fatal(err)
					}
					masked := strings.Join(tokens, " | ")
					assertEncodedJSONRestore(t, path, masked, text, r.Restore)
					assertEncodedStreamRestore(t, path, masked, text, r.Restore, false)
					if path == "/v1/responses" {
						assertEncodedStreamRestore(t, path, masked, text, r.Restore, true)
					}
					r.Restore.Release()
				}
			}
		})
	}
}

func assertEncodedJSONRestore(t *testing.T, path, masked, expected string, view *privacy.RestoreView) {
	t.Helper()
	var response any = map[string]any{"content": []any{map[string]string{"type": "text", "text": masked}, map[string]any{"type": "tool_use", "input": map[string]string{"password": masked}}}}
	if path == "/v1/responses" {
		args, _ := json.Marshal(map[string]string{"password": masked})
		response = map[string]any{"output": []any{map[string]any{"type": "message", "content": []map[string]string{{"type": "output_text", "text": masked}}}, map[string]string{"type": "function_call", "arguments": string(args)}}}
	}
	raw, _ := json.Marshal(response)
	delivery, err := restorestream.RestoreJSON(path, raw, view)
	if err != nil {
		t.Fatal(err)
	}
	text := gjson.GetBytes(delivery.Body, "content.0.text").Str
	tool := gjson.GetBytes(delivery.Body, "content.1.input.password").Str
	if path == "/v1/responses" {
		text = gjson.GetBytes(delivery.Body, "output.0.content.0.text").Str
		tool = gjson.Get(gjson.GetBytes(delivery.Body, "output.1.arguments").Str, "password").Str
	}
	if text != expected || tool != expected {
		t.Fatal("响应正文或工具参数未按命中的编码形式回填", path)
	}
}

// 占位符分散在多个 delta 中，并让 SSE 事件再次拆成网络片段。
func assertEncodedStreamRestore(t *testing.T, path, masked, expected string, view *privacy.RestoreView, websocket bool) {
	t.Helper()
	dst := httptest.NewRecorder()
	w := restorestream.New(dst, path, view)
	defer w.Release()
	var delivered []string
	send := func(event map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(event)
		if websocket {
			if err := w.WriteMessage(raw, func(frame []byte) error { delivered = append(delivered, string(frame)); return nil }); err != nil {
				t.Fatal(err)
			}
			return
		}
		wire := append(append([]byte("data: "), raw...), '\n', '\n')
		for i := 0; i < len(wire); i += 13 {
			if _, err := w.Write(wire[i:min(i+13, len(wire))]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if path == "/v1/messages" {
		send(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
	}
	for i := 0; i < len(masked); i += 7 {
		chunk := masked[i:min(i+7, len(masked))]
		if path == "/v1/messages" {
			send(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": chunk}})
		} else {
			send(map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "synthetic-message", "delta": chunk})
		}
	}
	if path == "/v1/messages" {
		send(map[string]any{"type": "content_block_stop", "index": 0})
		send(map[string]any{"type": "message_stop"})
	} else {
		send(map[string]any{"type": "response.output_text.done", "output_index": 0, "content_index": 0, "item_id": "synthetic-message", "text": masked})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{}}})
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if !websocket {
		for _, line := range strings.Split(dst.Body.String(), "\n") {
			if raw, ok := strings.CutPrefix(line, "data: "); ok {
				delivered = append(delivered, raw)
			}
		}
	}
	var text strings.Builder
	for _, event := range delivered {
		field := "delta.text"
		if path == "/v1/responses" {
			field = "delta"
		}
		text.WriteString(gjson.Get(event, field).Str)
	}
	if text.String() != expected {
		t.Fatalf("分片回填未保留编码形式: path=%s websocket=%t got=%q", path, websocket, text.String())
	}
}

func TestPrivacyExactVariantsFollowUpdatesAndDisable(t *testing.T) {
	s := newTestPrivacyService(t)
	enableRedactMode(t, s)
	ctx := context.Background()
	r, err := s.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{Enabled: true, Name: "合成密码", SecretValue: "A+/:?&= Z!", Category: "password"})
	if err != nil {
		t.Fatal(err)
	}
	const encoded = "QSsvOj8mPSBaIQ=="
	if !s.TestText(privacy.Request{}, encoded).Changed {
		t.Fatal("创建后变体未生效")
	}
	r.Enabled = false
	if _, err = s.UpdateExactSecret(ctx, r.ID, r); err != nil || s.TestText(privacy.Request{}, encoded).Changed {
		t.Fatal("禁用后变体仍生效", err)
	}
	r.Enabled, r.SecretValue = true, "changed-passphrase"
	if _, err = s.UpdateExactSecret(ctx, r.ID, r); err != nil || s.TestText(privacy.Request{}, encoded).Changed || !s.TestText(privacy.Request{}, "Y2hhbmdlZC1wYXNzcGhyYXNl").Changed {
		t.Fatal("编辑原值后变体未随快照更新", err)
	}
}
