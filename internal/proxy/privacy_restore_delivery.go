package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/tracking"
)

// 日志仅使用已知的媒体类型，不记录上游自定义头值或参数。
func restoreMediaType(header http.Header) string {
	values := header.Values("Content-Type")
	if len(values) == 0 {
		return "missing"
	}
	if len(values) != 1 {
		return "invalid"
	}
	media, _, err := mime.ParseMediaType(values[0])
	if err != nil {
		return "invalid"
	}
	switch media {
	case "text/event-stream", "application/json", "text/plain", "text/html", "application/octet-stream":
		return media
	default:
		return "other"
	}
}

// 每条参与回填的流只记一次。计数写进消息正文，以便在系统日志中搜索、复制。
func (sp *StreamProcessor) logRestoreDelivery(resp *http.Response, path, media string, writer *restorestream.Writer, err error) {
	var counts restorestream.DeliveryCounts
	if writer != nil {
		counts = writer.Delivered()
	}
	status := "completed"
	switch {
	case errors.Is(err, restorestream.ErrDelivery):
		status = "failed"
	case sp.lastAPIError != nil:
		status = "upstream_error"
	case sp.downstreamDisconnected || err != nil || writer != nil && !sp.tokenParser.GetStreamCompleteness().IsComplete:
		status = "interrupted"
	}
	reason := ""
	if errors.Is(err, restorestream.ErrResponseFormat) {
		reason = "unexpected_response_format"
	}
	reportPrivacyDelivery(resp, sp.requestID, path, media, status, reason, counts)
}

func reportPrivacyDelivery(resp *http.Response, id, path, media, status, reason string, counts restorestream.DeliveryCounts) {
	summary := handlers.RestoreSummary{Status: status, Reason: reason, Text: counts.Text, Tool: counts.Tool, UnknownText: counts.UnknownText, UnknownTool: counts.UnknownTool}
	handlers.RecordRestoreDelivery(resp, summary)
	log := slog.Info
	if status != "completed" {
		log = slog.Warn
	}
	log(fmt.Sprintf("[响应回填] [%s] %s，已写出 文本=%d 工具参数=%d，未还原 正文=%d 工具参数=%d", id, status, counts.Text, counts.Tool, counts.UnknownText, counts.UnknownTool), "path", path, "upstream_content_type", media, "reason", reason)
}

func (h *Handler) readRegularResponse(resp *http.Response, requestID string) ([]byte, error) {
	if path, view := handlers.ResponseRestore(resp); view != nil && path != "/v1/responses/compact" {
		// 解码器只关闭自身，外层在交付计数确认后释放真实响应及字典视图。
		reader := *resp
		reader.Body = io.NopCloser(resp.Body)
		body, err := readPrivacyRegularResponse(&reader)
		if err != nil && !errors.Is(err, restorestream.ErrDelivery) {
			reportPrivacyDelivery(resp, requestID, path, restoreMediaType(resp.Header), "interrupted", "upstream_read_error", restorestream.DeliveryCounts{})
		} else if err == nil && len(body) == 0 {
			reportPrivacyDelivery(resp, requestID, path, restoreMediaType(resp.Header), "upstream_error", "empty_response_body", restorestream.DeliveryCounts{})
		}
		return body, err
	}
	return h.responseProcessor.ProcessResponseBody(resp)
}

func failRegularRestore(w http.ResponseWriter, resp *http.Response, lm *RequestLifecycleManager, usage *tracking.TokenUsage) error {
	path, _ := handlers.ResponseRestore(resp)
	reportPrivacyDelivery(resp, lm.GetRequestID(), path, restoreMediaType(resp.Header), "failed", "invalid_response", restorestream.DeliveryCounts{})
	if usage != nil {
		lm.RecordTokensForFailedRequest(usage, "privacy_restore_failed")
	}
	lm.FailRequest("privacy_restore_failed", "无法完整回填上游响应", http.StatusBadGateway)
	http.Error(w, "无法完整回填上游响应", http.StatusBadGateway)
	return restorestream.ErrDelivery
}

// writeRegularResponse 在成功状态和正文写出前完整验证，按客户端真正收到的结果更新统计。
func (h *Handler) writeRegularResponse(w http.ResponseWriter, resp *http.Response, lm *RequestLifecycleManager, original []byte, usage *tracking.TokenUsage) error {
	path, view := handlers.ResponseRestore(resp)
	body := original
	var delivery *restorestream.JSONDelivery
	if view != nil && path != "/v1/responses/compact" {
		d, err := restorestream.RestoreJSON(path, body, view)
		if err != nil {
			return failRegularRestore(w, resp, lm, usage)
		}
		delivery = d
		body = d.Body
	}
	h.responseProcessor.CopyResponseHeaders(resp, w)
	w.WriteHeader(resp.StatusCode)
	n, err := w.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}

	if delivery != nil {
		status := "interrupted"
		var counts restorestream.DeliveryCounts
		if err == nil {
			status = "completed"
			counts = delivery.Confirm()
		}
		reportPrivacyDelivery(resp, lm.GetRequestID(), path, restoreMediaType(resp.Header), status, "", counts)
	}
	if view != nil && err != nil {
		lm.CancelRequest("客户端中断响应交付", usage)
		return errors.Join(context.Canceled, err)
	}
	return err
}

// 已交付部分 SSE 后使用协议错误事件收尾。原始上游用量独立结算，恢复失败不触发换号或冷却。
func failRestoreDelivery(w http.ResponseWriter, f http.Flusher, lm *RequestLifecycleManager, usage *tracking.TokenUsage, codex bool) {
	if usage != nil {
		lm.RecordTokensForFailedRequest(usage, "privacy_restore_failed")
	}
	lm.FailRequest("privacy_restore_failed", "无法完整回填上游响应", http.StatusBadGateway)
	payload := `{"type":"error","error":{"type":"privacy_restore_failed","message":"无法完整回填上游响应"}}`
	if codex {
		payload = `{"type":"error","code":"privacy_restore_failed","message":"无法完整回填上游响应"}`
	}
	_, _ = io.WriteString(w, "event: error\ndata: "+payload+"\n\n")
	f.Flush()
}
