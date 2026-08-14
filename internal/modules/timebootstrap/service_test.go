package timebootstrap

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"
)

type testSigner struct{ key *ecdsa.PrivateKey }

func (s testSigner) Public() crypto.PublicKey { return &s.key.PublicKey }

func (s testSigner) Sign(random io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil || opts.HashFunc() != crypto.SHA256 {
		return nil, errors.New("SHA-256 signer opts required")
	}
	return ecdsa.SignASN1(random, s.key, digest)
}

type testClockHealth bool

func (health testClockHealth) Healthy() bool { return bool(health) }

func TestBootstrapBindsSignedTimeToExactNonceAndChipUID(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	service, err := NewService("time-staging-g1", testSigner{key}, func() time.Time { return now }, testClockHealth(true))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	request := Request{
		Version: ProtocolVersion,
		ChipUID: []byte{0xb5, 0xeb, 0x3f, 0x00, 0xad, 0xc1},
		Nonce:   bytes.Repeat([]byte{0x42}, NonceSize),
	}

	response, err := service.Bootstrap(context.Background(), request)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	payload, err := Verify(&key.PublicKey, []byte("time-staging-g1"), request, response.COSESign1)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if payload.Domain != Domain || payload.Operation != Operation ||
		payload.IssuedAtUnix != now.Unix() || payload.ExpiresAtUnix != now.Add(MaxValidity).Unix() {
		t.Fatalf("unexpected signed payload: %+v", payload)
	}
}

func TestVerifyRejectsAnotherNonce(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService("time-staging-g1", testSigner{key}, time.Now, testClockHealth(true))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ProtocolVersion, []byte{1, 2, 3, 4, 5, 6}, bytes.Repeat([]byte{0x11}, NonceSize)}
	response, err := service.Bootstrap(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Nonce[0] ^= 0xff
	if _, err := Verify(&key.PublicKey, []byte("time-staging-g1"), request, response.COSESign1); !errors.Is(err, ErrResponseBinding) {
		t.Fatalf("Verify error=%v, want ErrResponseBinding", err)
	}
}

func TestBootstrapRejectsInvalidRequestAndUnhealthyClock(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService("time-staging-g1", testSigner{key}, time.Now, testClockHealth(false))
	if err != nil {
		t.Fatal(err)
	}
	valid := Request{ProtocolVersion, []byte{1, 2, 3, 4, 5, 6}, bytes.Repeat([]byte{0x22}, NonceSize)}
	if _, err := service.Bootstrap(context.Background(), valid); !errors.Is(err, ErrClockUnhealthy) {
		t.Fatalf("Bootstrap error=%v, want ErrClockUnhealthy", err)
	}
	valid.Nonce = valid.Nonce[:1]
	if _, err := service.Bootstrap(context.Background(), valid); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Bootstrap error=%v, want ErrInvalidRequest", err)
	}
}
