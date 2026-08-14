package internalauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
)

const SignatureDomain = "ExploreX-OpenAPI-HMAC-v2"

var ErrInvalidCanonicalRequest = errors.New("invalid canonical request")

type SignatureRequest struct {
	APIKey    string
	Timestamp string
	Nonce     string
	Method    string
	Path      string
	RawQuery  string
	Body      []byte
	Signature string
}

func Sign(apiSecret string, request SignatureRequest) (string, error) {
	canonical, err := BuildCanonicalRequest(request)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(apiSecret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func Verify(apiSecret string, request SignatureRequest) bool {
	expected, err := Sign(apiSecret, request)
	if err != nil {
		return false
	}
	received, err := hex.DecodeString(request.Signature)
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(expected)
	return hmac.Equal(want, received)
}

func BuildCanonicalRequest(request SignatureRequest) (string, error) {
	if !isCanonicalLine(request.APIKey) || !validTimestamp(request.Timestamp) || !validNonce(request.Nonce) {
		return "", ErrInvalidCanonicalRequest
	}
	method := strings.ToUpper(request.Method)
	if request.Method != strings.TrimSpace(request.Method) || !validMethod(method) {
		return "", ErrInvalidCanonicalRequest
	}
	path, err := CanonicalizePath(request.Path)
	if err != nil {
		return "", ErrInvalidCanonicalRequest
	}
	query, err := CanonicalizeQuery(request.RawQuery)
	if err != nil {
		return "", ErrInvalidCanonicalRequest
	}
	bodyHash := sha256.Sum256(request.Body)
	return strings.Join([]string{
		SignatureDomain,
		request.APIKey,
		request.Timestamp,
		request.Nonce,
		method,
		path,
		query,
		hex.EncodeToString(bodyHash[:]),
	}, "\n"), nil
}

func CanonicalizePath(rawPath string) (string, error) {
	if rawPath == "" {
		return "/", nil
	}
	if !strings.HasPrefix(rawPath, "/") || strings.ContainsAny(rawPath, "?#\r\n") {
		return "", ErrInvalidCanonicalRequest
	}
	segments := strings.Split(rawPath, "/")
	for index, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return "", ErrInvalidCanonicalRequest
		}
		segments[index] = encodeRFC3986(decoded)
	}
	return strings.Join(segments, "/"), nil
}

func CanonicalizeQuery(rawQuery string) (string, error) {
	if rawQuery == "" {
		return "", nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", ErrInvalidCanonicalRequest
	}
	type pair struct{ key, value string }
	pairs := make([]pair, 0, len(values))
	for key, entries := range values {
		for _, value := range entries {
			pairs = append(pairs, pair{key: encodeRFC3986(key), value: encodeRFC3986(value)})
		}
	}
	sort.Slice(pairs, func(left, right int) bool {
		if pairs[left].key == pairs[right].key {
			return pairs[left].value < pairs[right].value
		}
		return pairs[left].key < pairs[right].key
	})
	encoded := make([]string, len(pairs))
	for index, item := range pairs {
		encoded[index] = item.key + "=" + item.value
	}
	return strings.Join(encoded, "&"), nil
}

func encodeRFC3986(value string) string {
	const hexChars = "0123456789ABCDEF"
	var encoded strings.Builder
	for index := 0; index < len(value); index++ {
		valueByte := value[index]
		if (valueByte >= 'a' && valueByte <= 'z') ||
			(valueByte >= 'A' && valueByte <= 'Z') ||
			(valueByte >= '0' && valueByte <= '9') || strings.ContainsRune("-._~", rune(valueByte)) {
			encoded.WriteByte(valueByte)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexChars[valueByte>>4])
		encoded.WriteByte(hexChars[valueByte&0x0f])
	}
	return encoded.String()
}

func isCanonicalLine(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n")
}

func validTimestamp(value string) bool {
	if len(value) != 20 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validMethod(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func validNonce(value string) bool {
	if len(value) < 16 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-._~", character) {
			continue
		}
		return false
	}
	return true
}
