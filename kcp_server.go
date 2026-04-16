package pget

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
)

type KCPServer struct {
	RootDir       string
	HTTPUpstream  string // optional, e.g. http://127.0.0.1:9093
	HTTPTimeout   time.Duration
	MaxHTTPBody   int64 // max bytes returned to client (base64 encoded)
	httpTransport http.RoundTripper
}

func (s *KCPServer) ListenAndServe(listenAddr string) error {
	if s.RootDir == "" {
		s.RootDir = "."
	}
	if s.HTTPTimeout <= 0 {
		s.HTTPTimeout = 30 * time.Second
	}
	if s.MaxHTTPBody <= 0 {
		s.MaxHTTPBody = 4 << 20 // 4MiB
	}
	if s.httpTransport == nil {
		s.httpTransport = http.DefaultTransport
	}
	ln, err := kcp.ListenWithOptions(listenAddr, nil, 0, 0)
	if err != nil {
		return err
	}
	defer ln.Close()

	for {
		sess, err := ln.AcceptKCP()
		if err != nil {
			return err
		}
		go s.handleSession(sess)
	}
}

func (s *KCPServer) handleSession(sess *kcp.UDPSession) {
	defer sess.Close()

	sess.SetNoDelay(1, 20, 2, 1)
	_ = sess.SetMtu(1400)
	sess.SetWindowSize(4096, 4096)
	sess.SetStreamMode(true)
	sess.SetACKNoDelay(true)
	_ = sess.SetReadDeadline(time.Now().Add(30 * time.Second))
	_ = sess.SetWriteDeadline(time.Now().Add(5 * time.Minute))

	br := bufio.NewReader(sess)
	bw := bufio.NewWriter(sess)

	reqFrame, err := readFrame(br)
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpMetaResponse{OK: false, Err: err.Error()}))
		_ = bw.Flush()
		return
	}

	var env kcpEnvelope
	if err := json.Unmarshal(reqFrame, &env); err != nil {
		_ = writeFrame(bw, mustJSON(kcpMetaResponse{OK: false, Err: err.Error()}))
		_ = bw.Flush()
		return
	}

	switch env.Type {
	case kcpMsgTypeMeta:
		s.handleMeta(reqFrame, bw)
		_ = bw.Flush()
	case kcpMsgTypeRange:
		s.handleRange(sess, reqFrame, br, bw)
		_ = bw.Flush()
	case kcpMsgTypeHTTP:
		s.handleHTTP(reqFrame, bw)
		_ = bw.Flush()
	case kcpMsgTypeAM:
		s.handleAlertmanager(reqFrame, bw)
		_ = bw.Flush()
	default:
		_ = writeFrame(bw, mustJSON(kcpMetaResponse{OK: false, Err: "unknown message type"}))
		_ = bw.Flush()
	}
}

func (s *KCPServer) handleMeta(frame []byte, bw *bufio.Writer) {
	var req struct {
		Type string `json:"type"`
		kcpMetaRequest
	}
	if err := json.Unmarshal(frame, &req); err != nil {
		_ = writeFrame(bw, mustJSON(kcpMetaResponse{OK: false, Err: err.Error()}))
		return
	}

	full, err := s.safeJoin(req.Path)
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpMetaResponse{OK: false, Err: err.Error()}))
		return
	}

	fi, err := os.Stat(full)
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpMetaResponse{OK: false, Err: err.Error()}))
		return
	}
	if fi.IsDir() {
		_ = writeFrame(bw, mustJSON(kcpMetaResponse{OK: false, Err: "path is a directory"}))
		return
	}

	_ = writeFrame(bw, mustJSON(kcpMetaResponse{
		OK:      true,
		Name:    filepath.Base(full),
		Size:    fi.Size(),
		ModTime: fi.ModTime().Unix(),
	}))
}

func (s *KCPServer) handleRange(sess *kcp.UDPSession, frame []byte, br *bufio.Reader, bw *bufio.Writer) {
	var req struct {
		Type string `json:"type"`
		kcpRangeRequest
	}
	if err := json.Unmarshal(frame, &req); err != nil {
		_ = writeFrame(bw, mustJSON(kcpRangeResponse{OK: false, Err: err.Error()}))
		return
	}
	if req.Path == "" || req.Length <= 0 || req.Offset < 0 {
		_ = writeFrame(bw, mustJSON(kcpRangeResponse{OK: false, Err: "invalid range request"}))
		return
	}

	logger.Debug("range request",
		slog.String("path", req.Path),
		slog.Int64("offset", req.Offset),
		slog.Int64("len", req.Length),
	)

	full, err := s.safeJoin(req.Path)
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpRangeResponse{OK: false, Err: err.Error()}))
		return
	}

	f, err := os.Open(full)
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpRangeResponse{OK: false, Err: err.Error()}))
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpRangeResponse{OK: false, Err: err.Error()}))
		return
	}

	if req.Offset >= fi.Size() {
		_ = writeFrame(bw, mustJSON(kcpRangeResponse{OK: false, Err: "offset out of range"}))
		return
	}
	maxLen := fi.Size() - req.Offset
	if req.Length > maxLen {
		req.Length = maxLen
	}

	if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
		_ = writeFrame(bw, mustJSON(kcpRangeResponse{OK: false, Err: err.Error()}))
		return
	}

	if err := writeFrame(bw, mustJSON(kcpRangeResponse{OK: true})); err != nil {
		return
	}
	if err := bw.Flush(); err != nil {
		return
	}

	n, _ := io.CopyN(bw, f, req.Length)
	_ = bw.Flush()

	logger.Debug("range sent",
		slog.String("path", req.Path),
		slog.Int64("offset", req.Offset),
		slog.Int64("len", req.Length),
		slog.Int64("sent", n),
	)

	// Wait for client ACK before closing session, to avoid dropping in-flight data.
	_ = sess.SetReadDeadline(time.Now().Add(30 * time.Second))
	ackFrame, err := readFrame(br)
	if err != nil {
		logger.Warn("range ack read failed",
			slog.String("path", req.Path),
			slog.Int64("offset", req.Offset),
			slog.String("err", err.Error()),
		)
		return
	}
	var ack kcpAck
	if err := json.Unmarshal(ackFrame, &ack); err != nil {
		logger.Warn("range ack unmarshal failed",
			slog.String("path", req.Path),
			slog.Int64("offset", req.Offset),
			slog.String("err", err.Error()),
		)
		return
	}
	logger.Debug("range ack",
		slog.String("path", req.Path),
		slog.Int64("offset", req.Offset),
		slog.Int64("len", req.Length),
		slog.Bool("ok", ack.OK),
	)
}

func (s *KCPServer) safeJoin(rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	if rel == "." || rel == "" {
		return "", fmt.Errorf("invalid path")
	}

	rootAbs, err := filepath.Abs(s.RootDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rootAbs, rel)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return fullAbs, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (s *KCPServer) handleHTTP(frame []byte, bw *bufio.Writer) {
	var req kcpHTTPReq
	if err := json.Unmarshal(frame, &req); err != nil {
		_ = writeFrame(bw, mustJSON(kcpHTTPResp{Type: kcpMsgTypeHTTPR, ID: "", Status: 400, Err: err.Error()}))
		return
	}

	body, err := decodeB64(req.BodyB64)
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpHTTPResp{Type: kcpMsgTypeHTTPR, ID: req.ID, Status: 400, Err: "invalid body_b64"}))
		return
	}

	var status int
	var headers map[string]string
	var respBody []byte
	if s.HTTPUpstream != "" {
		status, headers, respBody, err = s.proxyHTTP(req.Method, req.Path, req.Headers, body)
		if err != nil {
			_ = writeFrame(bw, mustJSON(kcpHTTPResp{Type: kcpMsgTypeHTTPR, ID: req.ID, Status: 502, Err: err.Error()}))
			return
		}
	} else {
		status, headers, respBody = s.routeHTTP(req.Method, req.Path, req.Headers, body)
	}
	resp := kcpHTTPResp{
		Type:    kcpMsgTypeHTTPR,
		ID:      req.ID,
		Status:  status,
		Headers: headers,
	}
	if len(respBody) > 0 {
		resp.BodyB64 = base64Encode(respBody)
	}
	_ = writeFrame(bw, mustJSON(resp))
}

func (s *KCPServer) handleAlertmanager(frame []byte, bw *bufio.Writer) {
	var msg kcpAlertmanagerMsg
	if err := json.Unmarshal(frame, &msg); err != nil {
		_ = writeFrame(bw, mustJSON(kcpOKResp{OK: false, Err: err.Error()}))
		return
	}
	payload, err := decodeB64(msg.BodyB64)
	if err != nil {
		_ = writeFrame(bw, mustJSON(kcpOKResp{OK: false, Err: "invalid body_b64"}))
		return
	}

	// Best-effort parse for logging.
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		logger.Warn("alertmanager payload parse failed", slog.String("err", err.Error()))
	} else {
		var alerts int
		if a, ok := parsed["alerts"].([]any); ok {
			alerts = len(a)
		}
		logger.Info("alertmanager received", slog.Int("alerts", alerts))
	}

	_ = writeFrame(bw, mustJSON(kcpOKResp{OK: true}))
}

func base64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func (s *KCPServer) routeHTTP(method, path string, headers map[string]string, body []byte) (int, map[string]string, []byte) {
	method = strings.ToUpper(method)
	if headers == nil {
		headers = map[string]string{}
	}

	switch {
	case method == "GET" && path == "/healthz":
		return 200, map[string]string{"content-type": "text/plain"}, []byte("ok\n")
	case method == "GET" && path == "/control/ping":
		return 200, map[string]string{"content-type": "text/plain"}, []byte("pong\n")
	case method == "POST" && path == "/alerts/alertmanager":
		// Accept Alertmanager webhook JSON; reuse same logic.
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			return 400, map[string]string{"content-type": "text/plain"}, []byte("invalid json\n")
		}
		var alerts int
		if a, ok := parsed["alerts"].([]any); ok {
			alerts = len(a)
		}
		logger.Info("alertmanager (http) received", slog.Int("alerts", alerts))
		return 202, map[string]string{"content-type": "text/plain"}, []byte("accepted\n")
	default:
		return 404, map[string]string{"content-type": "text/plain"}, []byte("not found\n")
	}
}

func (s *KCPServer) proxyHTTP(method, p string, headers map[string]string, body []byte) (int, map[string]string, []byte, error) {
	u, err := url.Parse(s.HTTPUpstream)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return 0, nil, nil, fmt.Errorf("invalid http upstream: %q", s.HTTPUpstream)
	}
	rel, err := url.Parse(p)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("invalid path: %q", p)
	}
	target := u.ResolveReference(rel)

	req, err := http.NewRequest(strings.ToUpper(method), target.String(), bytesReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		if strings.TrimSpace(k) == "" {
			continue
		}
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout:   s.HTTPTimeout,
		Transport: s.httpTransport,
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, s.MaxHTTPBody)
	b, _ := io.ReadAll(limited)

	hm := map[string]string{}
	// keep a few common headers; others can be added later if needed
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		hm["content-type"] = ct
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		hm["content-length"] = cl
	}
	return resp.StatusCode, hm, b, nil
}

func bytesReader(b []byte) io.Reader {
	if len(b) == 0 {
		return nil
	}
	return bytes.NewReader(b)
}
