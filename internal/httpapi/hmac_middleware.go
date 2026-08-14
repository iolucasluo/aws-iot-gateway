package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
)

const maxSignedRequestBodyBytes = 1 << 20

type internalAppPrincipalKey struct{}

func HMACMiddleware(verifier *internalauth.Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if verifier == nil {
			writeError(writer, http.StatusServiceUnavailable, "auth_unavailable", "internal app authentication is unavailable")
			return
		}
		body, err := readAndRestoreSignedBody(request)
		if err != nil {
			if errors.Is(err, errSignedBodyTooLarge) {
				writeError(writer, http.StatusRequestEntityTooLarge, "request_too_large", "signed request body exceeds 1 MiB")
			} else {
				writeError(writer, http.StatusBadRequest, "invalid_request", "unable to read signed request body")
			}
			return
		}
		principal, err := verifier.Authenticate(request.Context(), internalauth.SignatureRequest{
			APIKey:    request.Header.Get("X-Api-Key"),
			Timestamp: request.Header.Get("X-Timestamp"),
			Nonce:     request.Header.Get("X-Nonce"),
			Signature: request.Header.Get("X-Signature"),
			Method:    request.Method,
			Path:      request.URL.EscapedPath(),
			RawQuery:  request.URL.RawQuery,
			Body:      body,
		})
		if err != nil {
			switch {
			case errors.Is(err, internalauth.ErrNonceReplayed):
				writeError(writer, http.StatusConflict, "nonce_replayed", "request nonce was already used")
			case errors.Is(err, internalauth.ErrReplayProtectionUnavailable):
				writeError(writer, http.StatusServiceUnavailable, "replay_protection_unavailable", "request replay protection is unavailable")
			default:
				writeError(writer, http.StatusUnauthorized, "invalid_signature", "internal app signature is invalid")
			}
			return
		}
		ctx := context.WithValue(request.Context(), internalAppPrincipalKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func InternalAppPrincipal(ctx context.Context) (internalauth.Principal, bool) {
	principal, ok := ctx.Value(internalAppPrincipalKey{}).(internalauth.Principal)
	return principal, ok
}

var errSignedBodyTooLarge = errors.New("signed request body too large")

func readAndRestoreSignedBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxSignedRequestBodyBytes+1))
	_ = request.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > maxSignedRequestBodyBytes {
		return nil, errSignedBodyTooLarge
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	return body, nil
}
