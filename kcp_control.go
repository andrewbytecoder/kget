package pget

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ControlClient struct {
	Addr string
}

func (c *ControlClient) Do(ctx context.Context, method, path string, headers map[string]string, body []byte) (*kcpHTTPResp, error) {
	if c.Addr == "" {
		return nil, fmt.Errorf("kcp addr is empty")
	}
	req := kcpHTTPReq{
		Type:    kcpMsgTypeHTTP,
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Method:  strings.ToUpper(method),
		Path:    path,
		Headers: headers,
	}
	if len(body) > 0 {
		req.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}
	return kcpDoHTTP(ctx, c.Addr, &req)
}

func (c *ControlClient) SendAlertmanager(ctx context.Context, bodyJSON []byte) error {
	if c.Addr == "" {
		return fmt.Errorf("kcp addr is empty")
	}
	msg := kcpAlertmanagerMsg{
		Type:    kcpMsgTypeAM,
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		BodyB64: base64.StdEncoding.EncodeToString(bodyJSON),
	}
	return kcpSendAlertmanager(ctx, c.Addr, &msg)
}

func decodeB64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func mustJSONAny(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

