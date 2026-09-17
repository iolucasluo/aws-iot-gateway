package portalaccount

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id 参数，与 docs/architecture/platform-plane-v1.md §3 保持一致。
// 参数写进哈希串，后续调参时旧哈希仍可校验。
const (
	argonMemoryKiB   uint32 = 65536
	argonIterations  uint32 = 3
	argonParallelism uint8  = 2
	argonSaltBytes          = 16
	argonKeyBytes    uint32 = 32
)

// ErrInvalidPasswordHash 表示存量哈希串无法解析；调用方必须按校验失败处理，不得降级放行。
var ErrInvalidPasswordHash = errors.New("invalid password hash")

// HashPassword 返回标准编码的 Argon2id 串：$argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>。
func HashPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", ErrInvalidInput
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 使用哈希串自身记录的参数重算并做常量时间比较。
// 任何解析错误都返回 false：损坏的哈希不能被当成"匹配"。
func VerifyPassword(encoded, password string) bool {
	params, salt, key, err := decodePasswordHash(encoded)
	if err != nil {
		return false
	}
	computed := argon2.IDKey([]byte(password), salt, params.iterations, params.memoryKiB, params.parallelism, uint32(len(key)))
	return subtle.ConstantTimeCompare(computed, key) == 1
}

type argonParams struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
}

func decodePasswordHash(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	version, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v="))
	if err != nil || version != argon2.Version {
		return argonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params, err := decodeArgonParams(parts[3])
	if err != nil {
		return argonParams{}, nil, nil, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return argonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return argonParams{}, nil, nil, ErrInvalidPasswordHash
	}
	return params, salt, key, nil
}

func decodeArgonParams(field string) (argonParams, error) {
	var params argonParams
	seen := 0
	for _, part := range strings.Split(field, ",") {
		name, value, found := strings.Cut(part, "=")
		if !found {
			return argonParams{}, ErrInvalidPasswordHash
		}
		number, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return argonParams{}, ErrInvalidPasswordHash
		}
		switch name {
		case "m":
			params.memoryKiB = uint32(number)
			seen++
		case "t":
			params.iterations = uint32(number)
			seen++
		case "p":
			if number == 0 || number > 255 {
				return argonParams{}, ErrInvalidPasswordHash
			}
			params.parallelism = uint8(number)
			seen++
		default:
			return argonParams{}, ErrInvalidPasswordHash
		}
	}
	if seen != 3 || params.memoryKiB == 0 || params.iterations == 0 {
		return argonParams{}, ErrInvalidPasswordHash
	}
	return params, nil
}
