package pget

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
)

// kcpIOTimeout is a per-read sliding deadline. Too small will cause "stop-go" downloads due to retries.
var kcpIOTimeout = 20 * time.Second

func init() {
	// Optional override: PGET_KCP_IO_TIMEOUT_MS=20000
	if v := os.Getenv("PGET_KCP_IO_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			kcpIOTimeout = time.Duration(ms) * time.Millisecond
		}
	}
}

func kcpDial(ctx context.Context, addr string) (*kcp.UDPSession, *bufio.Reader, *bufio.Writer, error) {
	if addr == "" {
		return nil, nil, nil, fmt.Errorf("kcp addr is empty")
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	type dialResult struct {
		sess *kcp.UDPSession
		err  error
	}
	ch := make(chan dialResult, 1)
	go func() {
		sess, err := kcp.DialWithOptions(addr, nil, 0, 0)
		ch <- dialResult{sess: sess, err: err}
	}()

	var sess *kcp.UDPSession
	select {
	case <-dialCtx.Done():
		return nil, nil, nil, dialCtx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, nil, nil, r.err
		}
		sess = r.sess
	}

	sess.SetNoDelay(1, 20, 2, 1)
	_ = sess.SetMtu(1400)
	sess.SetWindowSize(4096, 4096)
	sess.SetStreamMode(true)
	// Faster ACK can help avoid stalls on lossy links.
	sess.SetACKNoDelay(true)
	_ = sess.SetReadDeadline(time.Now().Add(kcpIOTimeout))
	_ = sess.SetWriteDeadline(time.Now().Add(kcpIOTimeout))

	go func() {
		<-ctx.Done()
		_ = sess.Close()
	}()

	return sess, bufio.NewReader(sess), bufio.NewWriter(sess), nil
}

type kcpBodyReadCloser struct {
	r *bufio.Reader
	c net.Conn
}

func (k *kcpBodyReadCloser) Read(p []byte) (int, error) {
	// Sliding deadline prevents indefinite stalls.
	if dc, ok := k.c.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = dc.SetReadDeadline(time.Now().Add(kcpIOTimeout))
	}
	return k.r.Read(p)
}
func (k *kcpBodyReadCloser) Close() error               { return k.c.Close() }

func writeFrame(w io.Writer, payload []byte) error {
	var lenbuf [4]byte
	binary.BigEndian.PutUint32(lenbuf[:], uint32(len(payload)))
	if _, err := w.Write(lenbuf[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var lenbuf [4]byte
	if _, err := io.ReadFull(r, lenbuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenbuf[:])
	if n == 0 {
		return nil, fmt.Errorf("empty frame")
	}
	if n > 16*1024*1024 {
		return nil, fmt.Errorf("frame too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func kcpGetMeta(ctx context.Context, addr, path string) (*kcpMetaResponse, error) {
	sess, br, bw, err := kcpDial(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	b, err := kcpMarshalMetaReq(path)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(bw, b); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}

	respFrame, err := readFrame(br)
	if err != nil {
		return nil, err
	}
	var resp kcpMetaResponse
	if err := json.Unmarshal(respFrame, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		if resp.Err != "" {
			return nil, fmt.Errorf("kcp meta error: %s", resp.Err)
		}
		return nil, fmt.Errorf("kcp meta error")
	}
	return &resp, nil
}

func kcpGetRange(ctx context.Context, addr string, req *kcpRangeRequest) (io.ReadCloser, error) {
	sess, br, bw, err := kcpDial(ctx, addr)
	if err != nil {
		return nil, err
	}

	b, err := kcpMarshalRangeReq(req)
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := writeFrame(bw, b); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		_ = sess.Close()
		return nil, err
	}

	respFrame, err := readFrame(br)
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	var resp kcpRangeResponse
	if err := json.Unmarshal(respFrame, &resp); err != nil {
		_ = sess.Close()
		return nil, err
	}
	if !resp.OK {
		_ = sess.Close()
		if resp.Err != "" {
			return nil, fmt.Errorf("kcp range error: %s", resp.Err)
		}
		return nil, fmt.Errorf("kcp range error")
	}

	return &kcpRangeReadCloser{
		r:      br,
		bw:     bw,
		c:      sess,
		offset: req.Offset,
		length: req.Length,
	}, nil
}

type kcpRangeReadCloser struct {
	r      *bufio.Reader
	bw     *bufio.Writer
	c      net.Conn
	offset int64
	length int64
	acked  bool
}

func (k *kcpRangeReadCloser) Read(p []byte) (int, error) {
	// Sliding deadline prevents indefinite stalls.
	if dc, ok := k.c.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = dc.SetReadDeadline(time.Now().Add(kcpIOTimeout))
	}
	return k.r.Read(p)
}

func (k *kcpRangeReadCloser) Close() error {
	if !k.acked {
		ack := kcpAck{Type: kcpMsgTypeAck, OK: true, Offset: k.offset, Len: k.length}
		if b, err := json.Marshal(ack); err == nil {
			_ = writeFrame(k.bw, b)
			_ = k.bw.Flush()
		}
		k.acked = true
	}
	return k.c.Close()
}

func kcpDoHTTP(ctx context.Context, addr string, req *kcpHTTPReq) (*kcpHTTPResp, error) {
	sess, br, bw, err := kcpDial(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(bw, b); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}

	respFrame, err := readFrame(br)
	if err != nil {
		return nil, err
	}
	var resp kcpHTTPResp
	if err := json.Unmarshal(respFrame, &resp); err != nil {
		return nil, err
	}
	if resp.Type != kcpMsgTypeHTTPR {
		return nil, fmt.Errorf("unexpected response type: %s", resp.Type)
	}
	if resp.Err != "" {
		return &resp, fmt.Errorf(resp.Err)
	}
	return &resp, nil
}

func kcpSendAlertmanager(ctx context.Context, addr string, msg *kcpAlertmanagerMsg) error {
	sess, br, bw, err := kcpDial(ctx, addr)
	if err != nil {
		return err
	}
	defer sess.Close()

	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := writeFrame(bw, b); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	respFrame, err := readFrame(br)
	if err != nil {
		return err
	}
	var resp kcpOKResp
	if err := json.Unmarshal(respFrame, &resp); err != nil {
		return err
	}
	if !resp.OK {
		if resp.Err != "" {
			return fmt.Errorf(resp.Err)
		}
		return fmt.Errorf("alertmanager send failed")
	}
	return nil
}

