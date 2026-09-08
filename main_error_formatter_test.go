package main

import (
	"errors"
	"testing"
)

// Wails 默认只回传 err.Error() 字符串，前端拿到的不是 Error 实例，err.message 恒为
// undefined，所有 `err.message || '操作失败'` 都会静默退化成兜底文案。这个契约一旦
// 被改回字符串，账号删除守卫这类"拦住并说明原因"的提示会重新丢失原因。
func TestFormatWailsErrorCarriesMessage(t *testing.T) {
	want := "账号「venlacy」仍被 Codex 模型 gpt-6-astra 使用，请先在「模型路由」中取消绑定"

	payload, ok := formatWailsError(errors.New(want)).(map[string]any)
	if !ok {
		t.Fatalf("expected map payload so frontend can read err.message, got %T", formatWailsError(errors.New("x")))
	}
	if payload["message"] != want {
		t.Fatalf("unexpected message: %v", payload["message"])
	}
	if formatWailsError(nil) != nil {
		t.Fatal("nil error must stay nil so successful calls are unaffected")
	}
}
