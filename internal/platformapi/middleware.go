package platformapi

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

const (
	requestIDHeader    = "X-Request-Id"
	maxRequestIDLength = 128
	bearerPrefix       = "Bearer "
)

// requestID 复用调用方提供的安全 request id，否则生成一个；
// 它同时用于审计记录的 request_id。
func requestID(request *http.Request) string {
	if existing := strings.TrimSpace(request.Header.Get(requestIDHeader)); safeRequestID(existing) {
		return existing
	}
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(value)
}

func safeRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLength {
		return false
	}
	for _, symbol := range value {
		if symbol < 0x21 || symbol > 0x7e {
			return false
		}
	}
	return true
}

// corsPolicy 只回显白名单内的 origin；未配置白名单时不返回任何 CORS 头。
type corsPolicy struct {
	allowed map[string]struct{}
}

func newCORSPolicy(origins []string) corsPolicy {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowed[origin] = struct{}{}
	}
	return corsPolicy{allowed: allowed}
}

// apply 写入 CORS 头并返回该请求是否已被预检短路。
func (policy corsPolicy) apply(writer http.ResponseWriter, request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	if _, ok := policy.allowed[origin]; !ok {
		// 非白名单来源不返回 CORS 头，浏览器侧自然拒绝。
		return request.Method == http.MethodOptions
	}
	writer.Header().Set("Access-Control-Allow-Origin", origin)
	writer.Header().Set("Vary", "Origin")
	writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
	writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
	writer.Header().Set("Access-Control-Max-Age", "600")
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// bearerToken 只接受 Authorization: Bearer <token>。
func bearerToken(request *http.Request) string {
	header := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(header[len(bearerPrefix):])
}

// clientIP 只在显式配置了受信代理头时才读取转发头，否则使用连接地址。
// 未配置却读取转发头会让任何人伪造 IP 绕过按 IP 的限流。
func clientIP(request *http.Request, trustedProxyHeader string) string {
	if trustedProxyHeader != "" {
		if value := strings.TrimSpace(request.Header.Get(trustedProxyHeader)); value != "" {
			if index := strings.Index(value, ","); index >= 0 {
				value = value[:index]
			}
			if candidate := strings.TrimSpace(value); candidate != "" {
				return candidate
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(request.RemoteAddr)
	}
	return host
}
