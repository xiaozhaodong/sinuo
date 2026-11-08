package privacyrestorebench_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/restorestream"
)

func syntheticToken() string {
	mac := hmac.New(sha256.New, make([]byte, 32))
	mac.Write([]byte(value))
	return fixtureRestoreToken(hex.EncodeToString(mac.Sum(nil)[:16]))
}

// 通过正式请求规则建立映射。每条被测流有独立的查表引用及交付统计，共用一份进程字典。
func seedBenchDictionary(b testing.TB) *privacy.RestoreDict {
	b.Helper()
	d := privacy.NewRestoreDict()
	settings := privacy.DefaultSettings()
	settings.Mode = privacy.ModeRedact
	settings.ResponseRestoreEnabled = true
	rules, err := privacy.CompileRules([]privacy.Rule{{ID: 1, Enabled: true, Name: "bench", Priority: 1,
		MatchType: privacy.MatchTypeLiteral, Pattern: value, Placeholder: "[MASK]", Action: privacy.ActionRedact}})
	if err != nil {
		b.Fatal(err)
	}
	s := privacy.Snapshot{Settings: settings, Dict: d, RestoreKey: make([]byte, 32), Rules: rules}
	req := privacy.Request{Path: "/v1/messages", Method: "POST", UpstreamType: privacy.UpstreamTypeEndpoint, ContentType: "application/json", StreamingHTTP: true}
	res, err := s.Apply(req, []byte(`{"stream":true,"messages":[{"role":"user","content":`+jsonString(value)+`}]}`))
	if err != nil || res.Restore == nil || gjson.GetBytes(res.Body, "messages.0.content").Str != productionToken {
		b.Fatal("未建立合成映射", err)
	}
	if err := res.Restore.Publish(); err != nil {
		b.Fatal(err)
	}
	res.Restore.Release()
	return d
}

func benchResponseWriter(dst http.ResponseWriter, codex bool, dict *privacy.RestoreDict) (*restorestream.Writer, *privacy.RestoreView, error) {
	view, err := dict.Prepare()
	if err != nil {
		return nil, nil, err
	}
	if err := view.Publish(); err != nil {
		view.Release()
		return nil, nil, err
	}
	path := "/v1/messages"
	if codex {
		path = "/v1/responses"
	}
	return restorestream.New(dst, path, view), view, nil
}

func benchReplay(events [][]byte, dst http.ResponseWriter, codex bool, dict *privacy.RestoreDict) error {
	if dict != nil {
		w, view, err := benchResponseWriter(dst, codex, dict)
		if err != nil {
			return err
		}
		defer view.Release()
		dst = w
	}
	_, err := replay(events, dst, codex)
	return err
}

type firstVisibleSink struct {
	sink
	start time.Time
	delay time.Duration
}

func (s *firstVisibleSink) Write(p []byte) (int, error) {
	// 夹具的第一段可见字节出现在文本 delta。
	if s.delay == 0 && bytes.Contains(p, []byte("visible content")) {
		s.delay = time.Since(s.start)
	}
	return s.sink.Write(p)
}

func firstTextDelay(events [][]byte, codex bool, dict *privacy.RestoreDict) (float64, error) {
	dst := &firstVisibleSink{sink: sink{headers: make(http.Header)}, start: time.Now()}
	if err := benchReplay(events, dst, codex, dict); err != nil {
		return 0, err
	}
	if dst.delay == 0 {
		return 0, errors.New("未交付首段可见文本")
	}
	return float64(dst.delay.Nanoseconds()) / 1000, nil
}
