package proxy

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"testing"

	"sinuo/internal/proxy/restorestream"
)

func TestReadPrivacyRegularResponseLimitAfterGzip(t *testing.T) {
	for _, size := range []int{19, restorestream.MaxJSONBytes, restorestream.MaxJSONBytes + 1} {
		var encoded bytes.Buffer
		w := gzip.NewWriter(&encoded)
		if _, err := io.CopyN(w, repeatedByte('x'), int64(size)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		resp := &http.Response{Header: http.Header{"Content-Encoding": {"gzip"}}, Body: io.NopCloser(&encoded)}
		got, err := readPrivacyRegularResponse(resp)
		if size > restorestream.MaxJSONBytes {
			if !errors.Is(err, restorestream.ErrDelivery) || got != nil {
				t.Fatal("解压后超限的正文可被交付")
			}
		} else if err != nil || len(got) != size {
			t.Fatal(size, len(got), err)
		}
	}
}

type repeatedByte byte

func (v repeatedByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(v)
	}
	return len(p), nil
}
