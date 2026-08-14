package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/timebootstrap"
)

const (
	timeBootstrapIPLimit     = 60
	timeBootstrapGlobalLimit = 6000
	timeBootstrapRateWindow  = time.Minute
)

type TimeBootstrapService interface {
	Bootstrap(context.Context, timebootstrap.Request) (timebootstrap.Response, error)
}

type TimeBootstrapLimiter interface {
	Allow(context.Context, string, int, time.Duration) (bool, error)
}

func (s server) serveTimeBootstrap(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if s.timeBootstrap == nil || s.bootstrapRate == nil {
		writeError(writer, http.StatusServiceUnavailable, "time_bootstrap_unavailable", "time bootstrap is unavailable")
		return
	}
	clientIP := timeBootstrapClientIP(request)
	allowed, err := s.bootstrapRate.Allow(request.Context(), "time-bootstrap:ip:"+clientIP, timeBootstrapIPLimit, timeBootstrapRateWindow)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "time_bootstrap_unavailable", "time bootstrap is unavailable")
		return
	}
	if !allowed {
		writeError(writer, http.StatusTooManyRequests, "time_bootstrap_rate_limited", "time bootstrap rate limit exceeded")
		return
	}
	allowed, err = s.bootstrapRate.Allow(request.Context(), "time-bootstrap:global", timeBootstrapGlobalLimit, timeBootstrapRateWindow)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "time_bootstrap_unavailable", "time bootstrap is unavailable")
		return
	}
	if !allowed {
		writeError(writer, http.StatusTooManyRequests, "time_bootstrap_rate_limited", "time bootstrap rate limit exceeded")
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "invalid_time_bootstrap_content_type", "Content-Type must be application/json")
		return
	}
	var body struct {
		Version uint64 `json:"version"`
		ChipUID string `json:"chip_uid"`
		Nonce   []byte `json:"nonce"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_time_bootstrap_request", "time bootstrap request is invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_time_bootstrap_request", "time bootstrap request is invalid")
		return
	}
	chipUID, err := hex.DecodeString(body.ChipUID)
	if err != nil || len(chipUID) != timebootstrap.ChipUIDSize || len(body.Nonce) != timebootstrap.NonceSize {
		writeError(writer, http.StatusBadRequest, "invalid_time_bootstrap_request", "time bootstrap request is invalid")
		return
	}
	response, err := s.timeBootstrap.Bootstrap(request.Context(), timebootstrap.Request{
		Version: body.Version, ChipUID: chipUID, Nonce: body.Nonce,
	})
	if err != nil {
		if errors.Is(err, timebootstrap.ErrInvalidRequest) {
			writeError(writer, http.StatusBadRequest, "invalid_time_bootstrap_request", "time bootstrap request is invalid")
			return
		}
		if errors.Is(err, timebootstrap.ErrClockUnhealthy) {
			writeError(writer, http.StatusServiceUnavailable, "time_bootstrap_clock_unhealthy", "time bootstrap clock is unavailable")
			return
		}
		writeError(writer, http.StatusServiceUnavailable, "time_bootstrap_unavailable", "time bootstrap is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func timeBootstrapClientIP(request *http.Request) string {
	forwarded := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
	for index := len(forwarded) - 1; index >= 0; index-- {
		if candidate := strings.TrimSpace(forwarded[index]); net.ParseIP(candidate) != nil {
			return candidate
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	return "unknown"
}
