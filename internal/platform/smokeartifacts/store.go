// Package smokeartifacts provides a disposable loopback-only OTA upload double.
// It is used only by platform-plane-smoke, never by deployed gateway binaries.
package smokeartifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

const maxSize = 3 << 20

type grant struct {
	key, hash string
	size      int64
	expires   time.Time
}
type Store struct {
	server  *httptest.Server
	mu      sync.Mutex
	grants  map[string]grant
	objects map[string][]byte
}

func New() *Store {
	s := &Store{grants: make(map[string]grant), objects: make(map[string][]byte)}
	s.server = httptest.NewServer(http.HandlerFunc(s.upload))
	return s
}
func (s *Store) Close() { s.server.Close() }
func (s *Store) PrepareUpload(_ context.Context, key string, size int64, hash string, ttl time.Duration) (ota.UploadTarget, error) {
	if size <= 0 || size > maxSize {
		return ota.UploadTarget{}, fmt.Errorf("smoke artifact size out of range")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return ota.UploadTarget{}, err
	}
	path := fmt.Sprintf("/upload/%x", token)
	expires := time.Now().Add(ttl)
	s.mu.Lock()
	s.grants[path] = grant{key, hash, size, expires}
	s.mu.Unlock()
	return ota.UploadTarget{URL: s.server.URL + path, Method: http.MethodPut, Headers: map[string]string{"Content-Type": "application/octet-stream", "X-Smoke-Upload": "required"}, ExpiresAt: expires}, nil
}
func (s *Store) Verify(_ context.Context, key string, size int64, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok || int64(len(data)) != size || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
		return fmt.Errorf("smoke artifact integrity mismatch")
	}
	return nil
}
func (s *Store) DownloadURL(context.Context, string, time.Duration) (string, error) {
	return "", fmt.Errorf("smoke artifact downloads and device deployment are unsupported")
}
func (s *Store) upload(w http.ResponseWriter, r *http.Request) {
	// Only the three local portals may use this endpoint from a browser.
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := false
		for _, host := range []string{"127.0.0.1", "localhost"} {
			for _, port := range []string{"18173", "18180", "18181"} {
				if origin == "http://"+host+":"+port {
					allowed = true
				}
			}
		}
		if !allowed {
			http.Error(w, "origin forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "PUT")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Smoke-Upload")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "PUT required", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	g, ok := s.grants[r.URL.Path]
	s.mu.Unlock()
	if !ok || time.Now().After(g.expires) {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Smoke-Upload") != "required" || r.Header.Get("Content-Type") != "application/octet-stream" {
		http.Error(w, "unexpected upload headers", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.size))
	if err != nil || int64(len(data)) != g.size || fmt.Sprintf("%x", sha256.Sum256(data)) != g.hash {
		http.Error(w, "integrity mismatch", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.objects[g.key]; exists {
		http.Error(w, "overwrite forbidden", http.StatusConflict)
		return
	}
	s.objects[g.key] = data
	w.WriteHeader(http.StatusOK)
}
