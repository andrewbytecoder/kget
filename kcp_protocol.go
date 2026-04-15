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

