package proxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/tracking"
)

// 首次实际交付时才提交成功头，解压、协议探测或整包校验失败仍可返回 502。
type subscriptionRestoreWriter struct {
	http.ResponseWriter
	upstream *http.Response
	media    string
	started  bool
	writeErr error
}

func (w *subscriptionRestoreWriter) Write(data []byte) (int, error) {
	if !w.started {
		copySubscriptionResponseHeaders(w.Header(), w.upstream.Header)
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Encoding")
		stripRestoreValidators(w.Header())
		w.Header().Set("Content-Type", w.media)
		w.WriteHeader(w.upstream.StatusCode)
		w.started = true
	}
	n, err := w.ResponseWriter.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil && w.media == "text/event-stream" {
		err = http.NewResponseController(w.ResponseWriter).Flush()
		if errors.Is(err, http.ErrNotSupported) {
			err = nil
		}
	}
	w.writeErr = err
	return n, err
}

func deliverSubscriptionHTTP(w http.ResponseWriter, r *http.Request, resp *http.Response, path string, view *privacy.RestoreView, streamRequested bool, capture *subscriptionResponseCapture, responseBytes *int64, track *subscriptionRequestTracking) {
	var terminal *ParseResult
	capture.onResult = func(result *ParseResult) {
		if result != nil {
			track.observeResponseFields(result)
			if result.IsCompleted {
				terminal = result
			}
		}
	}
	out := &subscriptionRestoreWriter{ResponseWriter: w, upstream: resp}
	counts, err := writeSubscriptionRestore(r.Context(), out, view, streamRequested, capture, responseBytes, track)
	capture.Finish()
	status, reason := "completed", ""
	if err != nil {
		status, reason = "failed", "privacy_restore_failed"
		httpStatus := http.StatusBadGateway
		if out.writeErr != nil || r.Context().Err() != nil {
			status, reason = "interrupted", "subscription_response_interrupted"
			httpStatus = 499
		} else if errors.Is(err, restorestream.ErrTruncated) || errors.Is(err, io.ErrUnexpectedEOF) {
			status, reason = "interrupted", "subscription_response_interrupted"
		}
		if out.writeErr == nil && r.Context().Err() == nil {
			if !out.started {
				writeAccountPipelineError(w, httpStatus, reason, "unable to deliver the upstream response")
			} else if out.media == "text/event-stream" {
				_, _ = io.WriteString(out, "event: error\ndata: {\"type\":\"error\",\"code\":\""+reason+"\",\"message\":\"unable to deliver the upstream response\"}\n\n")
			}
		}
		var usage *tracking.TokenUsage
		if terminal != nil && terminal.HasTokenUsage {
			usage = terminal.TokenUsage
		}
		reportPrivacyDelivery(resp, track.requestID, path, restoreMediaType(resp.Header), status, reason, counts)
		track.settle("failed", reason, usage, &httpStatus)
		return
	}
	if terminal != nil && (terminal.IsCancelled || terminal.ErrorInfo != nil || terminal.Status == StatusErrorAPI) {
		status = "upstream_error"
	}
	reportPrivacyDelivery(resp, track.requestID, path, restoreMediaType(resp.Header), status, "", counts)
	track.ApplyParseResult(terminal)
}

func writeSubscriptionRestore(ctx context.Context, out *subscriptionRestoreWriter, view *privacy.RestoreView, streamRequested bool, capture *subscriptionResponseCapture, responseBytes *int64, track *subscriptionRequestTracking) (restorestream.DeliveryCounts, error) {
	var counts restorestream.DeliveryCounts
	resp := out.upstream
	media := restoreMediaType(resp.Header)
	// 原始传输字节在解压前计量。令解码器只拥有副本，真实响应由入口统一关闭。
	readerResp := *resp
	readerResp.Body = &subscriptionObservedBody{ReadCloser: io.NopCloser(resp.Body), bytes: responseBytes, onBytes: track.MarkResponseBytes}
	decoded, err := openPrivacyResponseReader(&readerResp)
	if err != nil {
		return counts, err
	}
	defer decoded.Close()
	var reader io.Reader = decoded
	if media == "application/json" || media == "missing" && !streamRequested {
		out.media = "application/json"
		body, err := io.ReadAll(io.LimitReader(decoded, restorestream.MaxJSONBytes+1))
		// 共用有界的上游原文，避免用量观察器复制第二份整包。
		capture.mode, capture.pending = "json", body
		if err != nil {
			return counts, err
		}
		if streamRequested {
			return counts, restorestream.ErrResponseFormat
		}
		delivery, err := restorestream.RestoreJSON("/v1/responses", body, view)
		if err != nil {
			return counts, err
		}
		if _, err := out.Write(delivery.Body); err != nil {
			return counts, err
		}
		return delivery.Confirm(), nil
	}
	if media == "missing" && streamRequested {
		reader, err = restorestream.OpenUntypedResponses(ctx, bufio.NewReader(reader))
		if err != nil {
			return counts, err
		}
	} else if media != "text/event-stream" {
		return counts, restorestream.ErrResponseFormat
	}
	out.media = "text/event-stream"
	track.SetStreaming(true)
	capture.mode = "sse"
	reader = io.TeeReader(reader, capture)
	stream := restorestream.New(out, "/v1/responses", view)
	defer stream.Release()
	buf := make([]byte, subscriptionCopyBufferSize)
	for !stream.Terminal() {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, err := stream.Write(buf[:n]); err != nil {
				return stream.Delivered(), err
			}
		}
		if readErr != nil {
			if readErr != io.EOF && !stream.Terminal() {
				return stream.Delivered(), readErr
			}
			break
		}
	}
	err = stream.Finish()
	return stream.Delivered(), err
}
