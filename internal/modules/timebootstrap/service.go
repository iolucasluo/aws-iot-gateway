// Package timebootstrap owns the narrow platform-signed clock bootstrap used
// before an RTC-less device can validate normal TLS certificates.
package timebootstrap

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/veraison/go-cose"
)

const (
	ProtocolVersion uint64 = 1
	Domain                 = "ExploreX-Time-Bootstrap-v1"
	Operation              = "bootstrap_time"
	ChipUIDSize            = 6
	NonceSize              = 32
	MaxValidity            = 60 * time.Second
	maxKeyIDBytes          = 64
)

var (
	ErrInvalidRequest  = errors.New("invalid time bootstrap request")
	ErrInvalidResponse = errors.New("invalid signed time bootstrap response")
	ErrResponseBinding = errors.New("signed time bootstrap response binding mismatch")
	ErrClockUnhealthy  = errors.New("time bootstrap clock unhealthy")
	canonicalCBOR      = mustCanonicalCBOR()
	strictCBOR         = mustStrictCBOR()
)

type ClockHealth interface{ Healthy() bool }

type systemClockHealth struct{ now func() time.Time }

// NewSystemClockHealth rejects signing when the process clock is obviously
// uninitialized. ECS/Fargate owns host time synchronization in staging.
func NewSystemClockHealth(now func() time.Time) ClockHealth {
	if now == nil {
		now = time.Now
	}
	return systemClockHealth{now: now}
}

func (health systemClockHealth) Healthy() bool {
	return health.now().UTC().After(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
}

type Request struct {
	Version uint64
	ChipUID []byte
	Nonce   []byte
}

type Payload struct {
	Version       uint64 `cbor:"0,keyasint"`
	Domain        string `cbor:"1,keyasint"`
	Operation     string `cbor:"2,keyasint"`
	ChipUID       []byte `cbor:"3,keyasint"`
	Nonce         []byte `cbor:"4,keyasint"`
	IssuedAtUnix  int64  `cbor:"5,keyasint"`
	ExpiresAtUnix int64  `cbor:"6,keyasint"`
}

type Response struct {
	COSESign1 []byte `json:"time_cose_sign1"`
}

type Service struct {
	keyID  string
	signer crypto.Signer
	now    func() time.Time
	health ClockHealth
}

func NewService(keyID string, signer crypto.Signer, now func() time.Time, health ClockHealth) (*Service, error) {
	keyID = strings.TrimSpace(keyID)
	publicKey, ok := publicP256Key(signer)
	if keyID == "" || len([]byte(keyID)) > maxKeyIDBytes || !ok || publicKey == nil || health == nil {
		return nil, ErrInvalidRequest
	}
	if now == nil {
		now = time.Now
	}
	return &Service{keyID: keyID, signer: signer, now: now, health: health}, nil
}

func (service *Service) Bootstrap(_ context.Context, request Request) (Response, error) {
	if service == nil || service.signer == nil || !validRequest(request) {
		return Response{}, ErrInvalidRequest
	}
	if !service.health.Healthy() {
		return Response{}, ErrClockUnhealthy
	}
	now := service.now().UTC().Truncate(time.Second)
	payload := Payload{
		Version: ProtocolVersion, Domain: Domain, Operation: Operation,
		ChipUID: bytes.Clone(request.ChipUID), Nonce: bytes.Clone(request.Nonce),
		IssuedAtUnix: now.Unix(), ExpiresAtUnix: now.Add(MaxValidity).Unix(),
	}
	encoded, err := canonicalCBOR.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("marshal signed time payload: %w", err)
	}
	sign1, err := signES256(service.signer, []byte(service.keyID), encoded)
	if err != nil {
		return Response{}, fmt.Errorf("sign time bootstrap payload: %w", err)
	}
	return Response{COSESign1: sign1}, nil
}

func Verify(publicKey crypto.PublicKey, keyID []byte, request Request, sign1 []byte) (Payload, error) {
	var payload Payload
	if !validRequest(request) || len(keyID) == 0 || len(sign1) == 0 {
		return payload, ErrInvalidRequest
	}
	var message cose.Sign1Message
	if err := message.UnmarshalCBOR(sign1); err != nil {
		return payload, fmt.Errorf("decode time bootstrap COSE: %w", err)
	}
	if err := strictCBOR.Unmarshal(message.Payload, &payload); err != nil {
		return Payload{}, fmt.Errorf("decode canonical time payload: %w", err)
	}
	reencoded, err := canonicalCBOR.Marshal(payload)
	if err != nil || !bytes.Equal(reencoded, message.Payload) {
		return Payload{}, ErrInvalidResponse
	}
	if err := verifyES256(publicKey, keyID, message.Payload, &message); err != nil {
		return Payload{}, err
	}
	if !validPayload(payload) {
		return Payload{}, ErrInvalidResponse
	}
	if !bytes.Equal(payload.ChipUID, request.ChipUID) || !bytes.Equal(payload.Nonce, request.Nonce) {
		return Payload{}, ErrResponseBinding
	}
	return payload, nil
}

func validRequest(request Request) bool {
	return request.Version == ProtocolVersion && len(request.ChipUID) == ChipUIDSize && len(request.Nonce) == NonceSize
}

func validPayload(payload Payload) bool {
	return payload.Version == ProtocolVersion && payload.Domain == Domain && payload.Operation == Operation &&
		len(payload.ChipUID) == ChipUIDSize && len(payload.Nonce) == NonceSize &&
		payload.IssuedAtUnix > 0 && payload.ExpiresAtUnix > payload.IssuedAtUnix &&
		payload.ExpiresAtUnix-payload.IssuedAtUnix <= int64(MaxValidity/time.Second)
}

func publicP256Key(signer crypto.Signer) (*ecdsa.PublicKey, bool) {
	if signer == nil {
		return nil, false
	}
	publicKey, ok := signer.Public().(*ecdsa.PublicKey)
	return publicKey, ok && publicKey.Curve == elliptic.P256()
}

func signES256(signer crypto.Signer, keyID, payload []byte) ([]byte, error) {
	coseSigner, err := cose.NewSigner(cose.AlgorithmES256, sha256Signer{Signer: signer})
	if err != nil {
		return nil, err
	}
	message := cose.NewSign1Message()
	message.Headers.Protected.SetAlgorithm(cose.AlgorithmES256)
	message.Headers.Protected[cose.HeaderLabelKeyID] = bytes.Clone(keyID)
	message.Payload = bytes.Clone(payload)
	if err := message.Sign(rand.Reader, nil, coseSigner); err != nil {
		return nil, err
	}
	if err := normalizeLowS(message.Signature, elliptic.P256()); err != nil {
		return nil, err
	}
	return message.MarshalCBOR()
}

func verifyES256(publicKey crypto.PublicKey, keyID, payload []byte, message *cose.Sign1Message) error {
	ecdsaKey, ok := publicKey.(*ecdsa.PublicKey)
	if !ok || ecdsaKey.Curve != elliptic.P256() || len(message.Headers.Protected) != 2 || len(message.Headers.Unprotected) != 0 {
		return ErrInvalidResponse
	}
	algorithm, err := message.Headers.Protected.Algorithm()
	actualKeyID, keyOK := message.Headers.Protected[cose.HeaderLabelKeyID].([]byte)
	if err != nil || algorithm != cose.AlgorithmES256 || !keyOK || !bytes.Equal(actualKeyID, keyID) ||
		!bytes.Equal(message.Payload, payload) || !rawSignatureIsLowS(message.Signature, ecdsaKey.Curve) {
		return ErrInvalidResponse
	}
	verifier, err := cose.NewVerifier(cose.AlgorithmES256, ecdsaKey)
	if err != nil {
		return err
	}
	if err := message.Verify(nil, verifier); err != nil {
		return fmt.Errorf("verify time bootstrap COSE: %w", err)
	}
	return nil
}

type sha256Signer struct{ crypto.Signer }

func (signer sha256Signer) Sign(random io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	return signer.Signer.Sign(random, digest, crypto.SHA256)
}

func normalizeLowS(signature []byte, curve elliptic.Curve) error {
	size := (curve.Params().N.BitLen() + 7) / 8
	if len(signature) != size*2 {
		return errors.New("invalid raw ECDSA signature length")
	}
	s := new(big.Int).SetBytes(signature[size:])
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(curve.Params().N), 1)
	if s.Cmp(halfOrder) > 0 {
		s.Sub(curve.Params().N, s).FillBytes(signature[size:])
	}
	return nil
}

func rawSignatureIsLowS(signature []byte, curve elliptic.Curve) bool {
	size := (curve.Params().N.BitLen() + 7) / 8
	if len(signature) != size*2 {
		return false
	}
	s := new(big.Int).SetBytes(signature[size:])
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(curve.Params().N), 1)
	return s.Sign() > 0 && s.Cmp(halfOrder) <= 0
}

func mustCanonicalCBOR() cbor.EncMode {
	options := cbor.CoreDetEncOptions()
	options.IndefLength = cbor.IndefLengthForbidden
	mode, err := options.EncMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func mustStrictCBOR() cbor.DecMode {
	mode, err := (cbor.DecOptions{
		DupMapKey: cbor.DupMapKeyEnforcedAPF, MaxNestedLevels: 8, MaxArrayElements: 16,
		MaxMapPairs: 16, IndefLength: cbor.IndefLengthForbidden, TagsMd: cbor.TagsForbidden,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField, UTF8: cbor.UTF8RejectInvalid,
	}).DecMode()
	if err != nil {
		panic(err)
	}
	return mode
}
