// Package credentialcipher 用 AES-256-GCM 封装项目级 API 凭证的 secret。
//
// KEK 由部署提供（AWS 走 Secrets Manager 注入，国内走 0600 挂载文件），
// 明文只在创建/轮换响应中出现一次；数据库里只有 密文 + kek_id。
// v1 只实现读取与写入，不实现自动重加密轮换。
package credentialcipher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// KeyBytes 是 AES-256 要求的 KEK 长度。
	KeyBytes       = 32
	nonceBytes     = 12
	maxKEKIDLength = 64
	// additionalData 把密文绑定到唯一用途，避免同一 KEK 下的密文被跨上下文复用。
	additionalData = "explorex-platform-credential-v1"
)

var (
	ErrInvalidKey       = errors.New("credential cipher requires a 32-byte key")
	ErrInvalidKEKID     = errors.New("credential cipher requires a safe kek id")
	ErrInvalidSecret    = errors.New("credential secret must not be empty")
	ErrCiphertextBroken = errors.New("credential secret ciphertext is not usable")
)

// Cipher 是并发安全的：cipher.AEAD 本身无状态，nonce 每次随机生成。
type Cipher struct {
	aead  cipher.AEAD
	kekID string
}

// New 用原始 KEK 字节构造。
func New(key []byte, kekID string) (*Cipher, error) {
	if len(key) != KeyBytes {
		return nil, ErrInvalidKey
	}
	kekID = strings.TrimSpace(kekID)
	if kekID == "" || len(kekID) > maxKEKIDLength || strings.ContainsAny(kekID, " \t\r\n:/") {
		return nil, ErrInvalidKEKID
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("configure credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("configure credential cipher: %w", err)
	}
	return &Cipher{aead: aead, kekID: kekID}, nil
}

// NewFromBase64 解析部署注入的 base64 KEK（标准或 URL 编码均可）。
func NewFromBase64(encoded, kekID string) (*Cipher, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, ErrInvalidKey
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, ErrInvalidKey
	}
	return New(key, kekID)
}

// KEKID 返回当前 KEK 标识，写入 api_credentials.kek_id。
func (cipher *Cipher) KEKID() string { return cipher.kekID }

// Seal 返回 nonce||ciphertext 形式的自包含密文。
func (cipher *Cipher) Seal(secret string) ([]byte, error) {
	if secret == "" {
		return nil, ErrInvalidSecret
	}
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	sealed := cipher.aead.Seal(nil, nonce, []byte(secret), []byte(additionalData))
	return append(nonce, sealed...), nil
}

// Open 解出 secret。任何结构、密钥或完整性错误都返回 ErrCiphertextBroken，
// 调用方必须按"凭证不可用"fail closed，不能降级放行。
func (cipher *Cipher) Open(sealed []byte) (string, error) {
	if len(sealed) <= nonceBytes {
		return "", ErrCiphertextBroken
	}
	nonce, payload := sealed[:nonceBytes], sealed[nonceBytes:]
	plaintext, err := cipher.aead.Open(nil, nonce, payload, []byte(additionalData))
	if err != nil {
		return "", ErrCiphertextBroken
	}
	return string(plaintext), nil
}
