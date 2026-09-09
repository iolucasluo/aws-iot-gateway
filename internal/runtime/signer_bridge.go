package runtime

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

var (
	ErrInvalidSignerInput = errors.New("invalid signer input")
	ErrSignerUnavailable  = errors.New("signer unavailable")
)

// CryptoSigner adapts the provider Signer port to the crypto.Signer contract
// used by the time bootstrap module. The provider receives an already hashed
// SHA-256 digest and must return an ASN.1 ECDSA signature for the injected
// P-256 public key.
type CryptoSigner struct {
	ctx       context.Context
	signer    provider.Signer
	publicKey *ecdsa.PublicKey
}

var _ crypto.Signer = (*CryptoSigner)(nil)

func NewCryptoSigner(ctx context.Context, signer provider.Signer, publicKey crypto.PublicKey) (*CryptoSigner, error) {
	if ctx == nil || signer == nil {
		return nil, fmt.Errorf("%w: context and provider are required", ErrInvalidSignerInput)
	}
	key, ok := publicKey.(*ecdsa.PublicKey)
	if !ok || key == nil || key.Curve != elliptic.P256() || key.X == nil || key.Y == nil {
		return nil, fmt.Errorf("%w: signer public key must be ECDSA P-256", ErrInvalidSignerInput)
	}
	return &CryptoSigner{ctx: ctx, signer: signer, publicKey: key}, nil
}

func (signer *CryptoSigner) Public() crypto.PublicKey {
	if signer == nil {
		return nil
	}
	return signer.publicKey
}

func (signer *CryptoSigner) Sign(_ io.Reader, digest []byte, options crypto.SignerOpts) ([]byte, error) {
	if signer == nil || signer.signer == nil || options == nil || options.HashFunc() != crypto.SHA256 || len(digest) != sha256.Size {
		return nil, ErrInvalidSignerInput
	}
	signature, err := signer.signer.Sign(signer.ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("%w: provider sign failed: %w", ErrSignerUnavailable, err)
	}
	if len(signature) == 0 || !ecdsa.VerifyASN1(signer.publicKey, digest, signature) {
		return nil, fmt.Errorf("%w: provider returned an invalid ECDSA signature", ErrSignerUnavailable)
	}
	return append([]byte(nil), signature...), nil
}

func (signer *CryptoSigner) Health(ctx context.Context) error {
	if signer == nil || signer.signer == nil || ctx == nil {
		return ErrSignerUnavailable
	}
	if err := signer.signer.Health(ctx); err != nil {
		return fmt.Errorf("%w: provider health check failed: %w", ErrSignerUnavailable, err)
	}
	return nil
}
