package service

import (
	"context"
	"encoding/json"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/privacy/configimport"

	"github.com/tidwall/gjson"
)

func TestPrivacyConfigImport_EnvHash_PersistsAndRedactsWholeValues(t *testing.T) {
	s := newTestPrivacyService(t)
	enableRedactMode(t, s)
	ctx := context.Background()
	const password = "alpha9beta42#suffix"
	const token = "x9y8z7w6v5u4#tail"
	content := "DB_PASSWORD=" + password + " # password comment\nexport TOKEN=" + token + "\t# token comment\n"
	preview, err := s.PreviewConfigImport(ctx, []configimport.File{{Name: ".env", Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.CancelConfigImport(preview.ID) })
	if len(preview.Files) != 1 || preview.Files[0].Error != "" || len(preview.Candidates) != 2 || len(suggestedIDs(preview)) != 2 {
		t.Fatal("带 # 的密码与 token 应完整解析并默认勾选")
	}
	result, err := s.CommitConfigImport(ctx, preview.ID, suggestedIDs(preview))
	if err != nil || result.Added != 2 || result.Skipped != 0 {
		t.Fatalf("导入失败: result=%+v error=%v", result, err)
	}
	for _, value := range []string{password, token} {
		if exists, err := s.ExactSecretExists(ctx, value); err != nil || !exists {
			t.Fatalf("入库值应保留 # 及其后缀: error=%v", err)
		}
	}

	text := "literal " + password + " | " + token + " here"
	for _, tc := range []struct {
		path, field, upstream string
		input                 any
	}{
		{"/v1/messages", "messages.0.content", privacy.UpstreamTypeEndpoint, map[string]any{"messages": []map[string]string{{"role": "user", "content": text}}}},
		{"/v1/responses", "input", privacy.UpstreamTypeAccount, map[string]any{"input": text}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body, err := json.Marshal(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.Apply(ctx, privacy.Request{Path: tc.path, UpstreamType: tc.upstream}, body)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Changed || result.HitCount != 2 || gjson.GetBytes(result.Body, tc.field).String() != "literal [敏感值] | [敏感值] here" {
				t.Fatalf("应完整脱敏密码与 token，不留下 # 后缀: changed=%v hits=%d", result.Changed, result.HitCount)
			}
		})
	}
}
