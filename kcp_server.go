package pget

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
)

type KCPServer struct {
	RootDir string
}

func (s *KCPServer) ListenAndServe(listenAddr string) error {
	if s.RootDir == "" {
		s.RootDir = "."
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

