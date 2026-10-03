package main

import (
	"encoding/json"
	"errors"
	"testing"
)

// v3 前端收到 Error 实例，message 来自 err.Error()；MarshalError 只负责序列化 cause。
// 这个契约一旦被改回无法携带 message 的格式，账号删除守卫这类"拦住并说明原因"
// 的提示会重新丢失原因。
func TestFormatWailsErrorCarriesMessage(t *testing.T) {
	want := "账号「venlacy」仍被 Codex 模型 gpt-6-astra 使用，请先在「模型路由」中取消绑定"

	payload := formatWailsError(errors.New(want))
	if payload["message"] != want {
		t.Fatalf("unexpected message: %v", payload["message"])
	}
	if formatWailsError(nil) != nil {
		t.Fatal("nil error must stay nil so successful calls are unaffected")
	}

	var envelope struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(marshalWailsError(errors.New(want)), &envelope); err != nil {
		t.Fatalf("marshalWailsError returned invalid JSON: %v", err)
	}
	if envelope.Message != want {
		t.Fatalf("unexpected marshaled message: %q", envelope.Message)
	}
}
