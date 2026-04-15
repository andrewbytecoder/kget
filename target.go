package pget

import (
	"context"
	"path/filepath"
)

type Target struct {
	Filename      string
	ContentLength int64
	RemotePath    string
}

func CheckKCP(ctx context.Context, kcpAddr, remotePath string) (*Target, error) {
	meta, err := kcpGetMeta(ctx, kcpAddr, remotePath)
	if err != nil {
		return nil, err
	}
	name := meta.Name
	if name == "" {
		name = filepath.Base(filepath.FromSlash(remotePath))
	}
	return &Target{
		Filename:      name,
		ContentLength: meta.Size,
		RemotePath:    remotePath,
	}, nil
}

