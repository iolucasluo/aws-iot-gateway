package awskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type fakeKMS struct {
	key      *ecdsa.PrivateKey
	metadata *kms.GetPublicKeyOutput
	lastSign *kms.SignInput
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeKMS{key: key, metadata: &kms.GetPublicKeyOutput{
		KeySpec: types.KeySpecEccNistP256, KeyUsage: types.KeyUsageTypeSignVerify,
		SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha256}, PublicKey: publicKey,
	}}
}

func (fake *fakeKMS) GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	return fake.metadata, nil
}

func (fake *fakeKMS) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	fake.lastSign = input
	signature, err := ecdsa.SignASN1(rand.Reader, fake.key, input.Message)
	return &kms.SignOutput{SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256, Signature: signature}, err
}

func TestSignerUsesKMSDigestModeAndVerifiesReturnedSignature(t *testing.T) {
	fake := newFakeKMS(t)
	signer, err := NewSigner(context.Background(), fake, "alias/time-staging")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("signed time"))
	if _, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256); err != nil {
		t.Fatal(err)
	}
	if fake.lastSign == nil || fake.lastSign.MessageType != types.MessageTypeDigest ||
		fake.lastSign.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha256 {
		t.Fatalf("KMS Sign input=%+v", fake.lastSign)
	}
}

func TestSignerRejectsWrongKMSKeyShape(t *testing.T) {
	fake := newFakeKMS(t)
	fake.metadata.KeySpec = types.KeySpecEccNistP384
	if _, err := NewSigner(context.Background(), fake, "alias/time-staging"); err == nil {
		t.Fatal("accepted non-P256 key")
	}
}
