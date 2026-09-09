package runtime

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestNewCryptoSignerRequiresP256PublicKeyAndProvider(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCryptoSigner(context.Background(), nil, &key.PublicKey); err == nil {
		t.Fatal("NewCryptoSigner() accepted a nil provider")
	}
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCryptoSigner(context.Background(), &signerProviderStub{}, &other.PublicKey); err == nil {
		t.Fatal("NewCryptoSigner() accepted a non-P256 public key")
	}
}

func TestCryptoSignerAcceptsOnlySHA256DigestAndCopiesSignature(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	providerStub := &signerProviderStub{signKey: key}
	signer, err := NewCryptoSigner(context.Background(), providerStub, &key.PublicKey)
	if err != nil {
		t.Fatalf("NewCryptoSigner() error = %v", err)
	}
	digest := sha256.Sum256([]byte("time payload"))
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if len(signature) == 0 || !ecdsa.VerifyASN1(&key.PublicKey, digest[:], signature) || providerStub.gotDigest != string(digest[:]) {
		t.Fatalf("Sign() signature/digest = %q/%x", signature, providerStub.gotDigest)
	}
	original := append([]byte(nil), signature...)
	providerStub.signature[0] = 'X'
	if string(signature) != string(original) {
		t.Fatal("Sign() returned provider-owned signature buffer")
	}
	if _, err := signer.Sign(rand.Reader, []byte("short"), crypto.SHA256); !errors.Is(err, ErrInvalidSignerInput) {
		t.Fatalf("Sign() short digest error = %v, want invalid input", err)
	}
	if _, err := signer.Sign(rand.Reader, digest[:], crypto.SHA384); !errors.Is(err, ErrInvalidSignerInput) {
		t.Fatalf("Sign() wrong hash error = %v, want invalid input", err)
	}
}

func TestCryptoSignerPropagatesProviderErrorAndHealth(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	providerStub := &signerProviderStub{signErr: errors.New("provider unavailable")}
	signer, err := NewCryptoSigner(context.Background(), providerStub, &key.PublicKey)
	if err != nil {
		t.Fatalf("NewCryptoSigner() error = %v", err)
	}
	digest := sha256.Sum256([]byte("time payload"))
	if _, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256); !errors.Is(err, providerStub.signErr) {
		t.Fatalf("Sign() error = %v, want provider error", err)
	}
	healthErr := errors.New("health unavailable")
	providerStub.healthErr = healthErr
	if err := signer.Health(context.Background()); !errors.Is(err, healthErr) {
		t.Fatalf("Health() error = %v, want provider error", err)
	}
	providerStub.signErr = nil
	providerStub.signature = []byte("invalid-signature")
	if _, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256); !errors.Is(err, ErrSignerUnavailable) {
		t.Fatalf("Sign() invalid provider signature error = %v, want unavailable", err)
	}
}

type signerProviderStub struct {
	signature []byte
	signKey   *ecdsa.PrivateKey
	gotDigest string
	signErr   error
	healthErr error
}

func (stub *signerProviderStub) Sign(_ context.Context, digest []byte) ([]byte, error) {
	stub.gotDigest = string(digest)
	if stub.signErr != nil {
		return nil, stub.signErr
	}
	if stub.signKey != nil {
		generated, err := ecdsa.SignASN1(rand.Reader, stub.signKey, digest)
		if err != nil {
			return nil, err
		}
		stub.signature = generated
	}
	return stub.signature, nil
}

func (stub *signerProviderStub) Health(context.Context) error { return stub.healthErr }
