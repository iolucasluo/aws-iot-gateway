package smokeartifacts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUploadContract(t *testing.T) {
	s := New()
	defer s.Close()
	body := "local artifact"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	target, err := s.PrepareUpload(context.Background(), "ota/test.bin", int64(len(body)), hash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	put := func(value string, auth bool, header bool) int {
		req, _ := http.NewRequest(http.MethodPut, target.URL, strings.NewReader(value))
		if header {
			for k, v := range target.Headers {
				req.Header.Set(k, v)
			}
		}
		if auth {
			req.Header.Set("Authorization", "Bearer test-only")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if err := s.Verify(context.Background(), "ota/test.bin", int64(len(body)), hash); err == nil {
		t.Fatal("missing object accepted")
	}
	for _, tc := range []struct {
		body         string
		auth, header bool
	}{{body, true, true}, {body, false, false}, {"bad", false, true}, {body + body, false, true}} {
		if status := put(tc.body, tc.auth, tc.header); status < 400 {
			t.Fatalf("invalid upload accepted: %d", status)
		}
	}
	if status := put(body, false, true); status != 200 {
		t.Fatalf("valid upload: %d", status)
	}
	if err := s.Verify(context.Background(), "ota/test.bin", int64(len(body)), hash); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(context.Background(), "ota/test.bin", int64(len(body)), strings.Repeat("0", 64)); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if status := put(body, false, true); status != 409 {
		t.Fatalf("overwrite status=%d", status)
	}
	expired, _ := s.PrepareUpload(context.Background(), "ota/expired.bin", 1, hash, -time.Second)
	expiredReq, _ := http.NewRequest(http.MethodPut, expired.URL, strings.NewReader("x"))
	resp, err := http.DefaultClient.Do(expiredReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal("expired upload accepted")
	}
}

func TestUploadPreflightAndLimits(t *testing.T) {
	s := New()
	defer s.Close()
	for _, size := range []int64{0, -1, maxSize + 1} {
		if _, err := s.PrepareUpload(context.Background(), "x", size, "hash", time.Minute); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
	target, err := s.PrepareUpload(context.Background(), "x", 1, "hash", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://127.0.0.1:18173", "https://untrusted.example"} {
		req, _ := http.NewRequest(http.MethodOptions, target.URL, nil)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if origin == "http://127.0.0.1:18173" {
			if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != origin || strings.Contains(strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers")), "authorization") {
				t.Fatal("unsafe or missing CORS response")
			}
		} else if resp.StatusCode != 403 {
			t.Fatal("foreign origin accepted")
		}
	}
}
