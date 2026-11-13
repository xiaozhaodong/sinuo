package restorestream

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sinuo/internal/privacy"
)

func TestWebSocketOpeningPrepareAndConfirm(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("a", 32) + "}}"
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			d := &recordingDictionary{token: token, original: "synthetic original"}
			w := New(nil, "/v1/responses", d)
			defer w.Release()
			msg, err := ValidateMessage([]byte(fmt.Sprintf(`{"type":"response.created","response":{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}}`, token)))
			if err != nil {
				t.Fatal(err)
			}
			out, err := w.PrepareOpeningMessage(msg)
			if err != nil || bytes.Contains(out, []byte(token)) || d.text != 0 || w.Delivered() != (DeliveryCounts{}) {
				t.Fatal("准备阶段发生交付或回填不完整", err)
			}
			want := errors.New("synthetic write error")
			err = w.DeliverPreparedMessage(out, func([]byte) error {
				if failure {
					return want
				}
				return nil
			})
			if failure && (!errors.Is(err, want) || d.text != 0) || !failure && (err != nil || d.text != 1 || w.Delivered().Text != 1) {
				t.Fatal("交付确认不准确", err, d.text)
			}
		})
	}
}

func TestWebSocketOpeningCapacityClassification(t *testing.T) {
	token := privacy.RestorePrefix + strings.Repeat("a", 32) + "}}"
	for _, kind := range []string{"expanded-value", "lookup", "protocol", "identity"} {
		t.Run(kind, func(t *testing.T) {
			d := &recordingDictionary{token: token, original: strings.Repeat("x", maxBytes+1)}
			items := fmt.Sprintf(`{"type":"message","content":[{"type":"output_text","text":%q}]}`, token)
			if kind == "lookup" {
				d.lookupErr = errors.New("local dictionary capacity")
			} else if kind == "protocol" {
				items = "42"
			}
			raw := `{"type":"response.created","response":{"id":"r","output":[` + items + `]}}`
			if kind == "identity" {
				raw = `{"type":"response.created","response_id":"other","response":{"id":"r"}}`
			}
			msg, err := ValidateMessage([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			w := New(nil, "/v1/responses", d)
			defer w.Release()
			out, err := w.PrepareOpeningMessage(msg)
			local := kind == "expanded-value" || kind == "lookup"
			if out != nil || !errors.Is(err, ErrDelivery) || errors.Is(err, ErrLocalCapacity) != local || d.text != 0 {
				t.Fatal("首帧错误分类或回滚边界错误", err)
			}
		})
	}
}

func TestWebSocketOpeningCountBoundaries(t *testing.T) {
	for _, kind := range []string{"items", "fields", "calls"} {
		for _, n := range []int{255, 256, 257} {
			t.Run(fmt.Sprintf("%s/%d", kind, n), func(t *testing.T) {
				var parts []string
				for j := 0; j < n; j++ {
					switch kind {
					case "items":
						parts = append(parts, fmt.Sprintf(`{"type":"message","id":"m%d"}`, j))
					case "calls":
						parts = append(parts, fmt.Sprintf(`{"type":"function_call","call_id":"c%d"}`, j))
					case "fields":
						parts = append(parts, `{"type":"output_text","text":"a"}`)
					}
				}
				items := strings.Join(parts, ",")
				if kind == "fields" {
					items = `{"type":"message","content":[` + items + `]}`
				}
				msg, err := ValidateMessage([]byte(`{"type":"response.created","response":{"id":"r","output":[` + items + `]}}`))
				if err != nil {
					t.Fatal(err)
				}
				w := New(nil, "/v1/responses", nil)
				defer w.Release()
				_, err = w.PrepareOpeningMessage(msg)
				if n <= 256 && err != nil || n > 256 && !errors.Is(err, ErrLocalCapacity) {
					t.Fatal("容量边界错误", err)
				}
			})
		}
	}
}

func TestWebSocketOpeningUsesDecodedEventType(t *testing.T) {
	for _, literal := range []string{`"response.created"`, `"response.\u0063reated"`, `" response.created "`, `"response.completed"`} {
		t.Run(literal, func(t *testing.T) {
			msg, err := ValidateMessage([]byte(`{"\u0074ype":` + literal + `,"response":{"id":"\ud800\u0041"}}`))
			if err != nil {
				t.Fatal(err)
			}
			w := New(nil, "/v1/responses", nil)
			defer w.Release()
			_, err = w.PrepareOpeningMessage(msg)
			if literal == `"response.completed"` {
				if !errors.Is(err, ErrDelivery) {
					t.Fatal("非首帧获得了准备权限")
				}
			} else if err != nil || w.responseID != "�A" {
				t.Fatal("首帧使用了不同的事件或归属解码", err)
			}
		})
	}
}
