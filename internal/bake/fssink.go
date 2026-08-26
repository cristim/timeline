package bake

import (
	"bytes"
	"context"
	"crypto/md5"
	"os"
	"path/filepath"
	"sync"
)

// FSSink writes artifacts to a local directory: the CI/static-hosting path
// (GitHub Pages, or any host that serves files - spec 04's host-agnostic
// note). Same change-detection semantics as the S3 sink.
type FSSink struct {
	Root string
	mu   sync.Mutex // serializes MkdirAll on shared parents
}

func (s *FSSink) Put(_ context.Context, key string, body []byte, _ string) (bool, error) {
	path := filepath.Join(s.Root, filepath.FromSlash(key))
	if old, err := os.ReadFile(path); err == nil {
		sum, oldSum := md5.Sum(body), md5.Sum(old)
		if bytes.Equal(sum[:], oldSum[:]) {
			return false, nil
		}
	}
	s.mu.Lock()
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	s.mu.Unlock()
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return false, err
	}
	return true, nil
}
