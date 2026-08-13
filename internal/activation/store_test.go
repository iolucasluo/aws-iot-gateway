package activation

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreRoundTripsOnlyPersistentActivationFields(t *testing.T) {
	t.Parallel()

	store := NewFileStore(filepath.Join(t.TempDir(), "activations.json"))
	want := []StoredRecord{{
		Activation: Activation{
			ID:        "activation-1",
			Method:    MethodBLE,
			User:      "user-42",
			Status:    StatusInstalling,
			ExpiresAt: time.Date(2026, 8, 13, 5, 0, 0, 0, time.UTC),
		},
		CodeHash: "sha256:stored-not-plaintext",
		CSRPEM:   "csr-pem",
		Installation: Installation{
			ActivationID:  "activation-1",
			DeviceID:      "aws-iot-demo-device-1",
			CertificateID: "cert-1",
			Certificate:   "certificate-pem",
			Endpoint:      "example-ats.iot.ap-southeast-1.amazonaws.com",
			ProofNonce:    "nonce",
		},
	}}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != 1 || got[0].Activation != want[0].Activation ||
		got[0].CodeHash != want[0].CodeHash || got[0].CSRPEM != want[0].CSRPEM ||
		got[0].Installation != want[0].Installation {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	if got[0].Activation.Code != "" {
		t.Fatal("Load() must not restore an activation code")
	}
}
