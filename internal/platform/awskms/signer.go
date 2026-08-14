// Package awskms adapts an AWS KMS P-256 SIGN_VERIFY key to crypto.Signer.
package awskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type API interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
}

type Signer struct {
	ctx       context.Context
	api       API
	keyID     string
	publicKey *ecdsa.PublicKey
}

func NewSigner(ctx context.Context, api API, keyID string) (*Signer, error) {
	keyID = strings.TrimSpace(keyID)
	if ctx == nil || api == nil || keyID == "" {
		return nil, fmt.Errorf("KMS signer requires context, client, and key ID")
	}
	output, err := api.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(keyID)})
	if err != nil {
		return nil, fmt.Errorf("get KMS public key: %w", err)
	}
	if output == nil || output.KeySpec != types.KeySpecEccNistP256 || output.KeyUsage != types.KeyUsageTypeSignVerify ||
		!supportsECDSASHA256(output.SigningAlgorithms) {
		return nil, fmt.Errorf("KMS key must be ECC_NIST_P256 SIGN_VERIFY with ECDSA_SHA_256")
	}
	parsed, err := x509.ParsePKIXPublicKey(output.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("parse KMS public key: %w", err)
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("KMS public key must be P-256")
	}
	return &Signer{ctx: ctx, api: api, keyID: keyID, publicKey: publicKey}, nil
}

func (signer *Signer) Public() crypto.PublicKey { return signer.publicKey }

func (signer *Signer) Sign(_ io.Reader, digest []byte, options crypto.SignerOpts) ([]byte, error) {
	if options == nil || options.HashFunc() != crypto.SHA256 || len(digest) != sha256.Size {
		return nil, fmt.Errorf("KMS signer accepts only SHA-256 digests")
	}
	output, err := signer.api.Sign(signer.ctx, &kms.SignInput{
		KeyId: aws.String(signer.keyID), Message: digest, MessageType: types.MessageTypeDigest,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return nil, fmt.Errorf("KMS sign digest: %w", err)
	}
	if output == nil || output.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha256 ||
		!ecdsa.VerifyASN1(signer.publicKey, digest, output.Signature) {
		return nil, fmt.Errorf("KMS returned an invalid ECDSA_SHA_256 signature")
	}
	return append([]byte(nil), output.Signature...), nil
}

func supportsECDSASHA256(algorithms []types.SigningAlgorithmSpec) bool {
	for _, algorithm := range algorithms {
		if algorithm == types.SigningAlgorithmSpecEcdsaSha256 {
			return true
		}
	}
	return false
}
