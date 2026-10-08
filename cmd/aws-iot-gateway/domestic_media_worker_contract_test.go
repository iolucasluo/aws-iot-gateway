package main

import (
	"os"
	"strings"
	"testing"
)

func TestDomesticRuntimeStartsLocalRecordingWorkers(t *testing.T) {
	source, err := os.ReadFile("domestic_runtime.go")
	if err != nil {
		t.Fatalf("read domestic runtime: %v", err)
	}
	text := string(source)

	// The domestic gateway owns the object store and the asset service, so it must also
	// run the workers that fill and retire device-local recordings. Without them the
	// thumbnail outbox stays pending forever, thumbnail_object_key never gets set, and
	// the recording list has no first-frame image.
	storeGuard := strings.Index(text, "if objectStore != nil {")
	if storeGuard < 0 {
		t.Fatal("domestic runtime must guard media wiring on the object store")
	}
	for _, want := range []string{
		"worker.NewLocalThumbnailWorker(",
		"runLocalThumbnailWorker(",
		"worker.NewLocalReconciliationWorker(",
		"runLocalReconciliationWorker(",
	} {
		at := strings.Index(text, want)
		if at < 0 {
			t.Fatalf("domestic runtime must start %s", want)
		}
		if at < storeGuard {
			t.Fatalf("%s must be wired where the object store is available", want)
		}
	}
}

func TestDomesticRuntimeReusesObjectStoreForPrivatePetPhotos(t *testing.T) {
	source, err := os.ReadFile("domestic_runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"config.PetPhotoPublicOrigin",
		"worker.NewPetPhotoWorker(",
		"runPetPhotoWorker(",
		"media.NewPetPhotoAccess(",
		"PetPhotos: petPhotoAccess",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("domestic private media missing %q", want)
		}
	}
}
