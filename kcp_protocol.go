package pget

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

const (
	kcpMsgTypeMeta  = "meta"
	kcpMsgTypeRange = "range"
	kcpMsgTypeAck   = "ack"
	kcpMsgTypeHTTP  = "http"
	kcpMsgTypeHTTPR = "http_resp"
	kcpMsgTypeAM    = "alertmanager"
)

type kcpEnvelope struct {
	Type string `json:"type"`
}

type kcpMetaRequest struct {
	Path string `json:"path"`
}

type kcpMetaResponse struct {
	OK      bool   `json:"ok"`
	Err     string `json:"err,omitempty"`
	Name    string `json:"name,omitempty"`
	Size    int64  `json:"size,omitempty"`
	ModTime int64  `json:"mod_time_unix,omitempty"`
}

type kcpRangeRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	// passthrough fields (kept for future logging/auditing)
	ClientUA string `json:"client_ua,omitempty"`
	Referer  string `json:"referer,omitempty"`
}

type kcpRangeResponse struct {
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

type kcpAck struct {
	Type   string `json:"type"`
	OK     bool   `json:"ok"`
	Offset int64  `json:"offset,omitempty"`
	Len    int64  `json:"len,omitempty"`
	Err    string `json:"err,omitempty"`
}

// kcpHTTPReq is a lightweight RESTful control request tunneled over KCP.
// Body is base64-encoded to keep this request in a single frame.
type kcpHTTPReq struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	BodyB64 string            `json:"body_b64,omitempty"`
	// BodyLen indicates the raw body byte length that follows the JSON frame on the KCP stream.
	// If BodyB64 is set, BodyLen is ignored.
	BodyLen int64 `json:"body_len,omitempty"`
}

type kcpHTTPResp struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	BodyB64 string            `json:"body_b64,omitempty"`
	Err     string            `json:"err,omitempty"`
}

// kcpAlertmanagerMsg carries Alertmanager webhook JSON.
type kcpAlertmanagerMsg struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	BodyB64 string `json:"body_b64"`
}

type kcpOKResp struct {
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

func kcpMarshalMetaReq(path string) ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		kcpMetaRequest
	}{
		Type: kcpMsgTypeMeta,
		kcpMetaRequest: kcpMetaRequest{
			Path: filepath.ToSlash(path),
		},
	})
}

func kcpMarshalRangeReq(req *kcpRangeRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("nil range request")
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		*kcpRangeRequest
	}{
		Type:           kcpMsgTypeRange,
		kcpRangeRequest: req,
	})
}

