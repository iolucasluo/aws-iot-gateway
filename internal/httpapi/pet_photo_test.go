package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type petPhotoHTTPRepo struct{ photo media.PetPhoto }

func (repo petPhotoHTTPRepo) GetPetPhoto(_ context.Context, _, _, _ string) (media.PetPhoto, error) {
	if repo.photo.ID == "" {
		return media.PetPhoto{}, media.ErrPetPhotoNotFound
	}
	return repo.photo, nil
}

type petPhotoHTTPStore struct{ bytes []byte }

func (store petPhotoHTTPStore) Download(context.Context, string, int64) ([]byte, error) {
	return store.bytes, nil
}

func TestPetPhotoReadRequiresCurrentBindingAndReturnsPrivateJPEG(t *testing.T) {
	now := time.UnixMilli(1_718_000_000_000)
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(repository, func() (string, error) { return "pet-photo-token", nil }, func() time.Time { return now }, nil)
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN); err != nil {
		t.Fatal(err)
	}
	photoAccess, err := media.NewPetPhotoAccess(petPhotoHTTPRepo{}, petPhotoHTTPStore{})
	if err != nil {
		t.Fatal(err)
	}
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService, PetPhotos: photoAccess})
	path := "/api/v1/openapi/devices/" + repository.binding.DeviceSN + "/pet-photos/018f0000-0000-7000-8000-000000000012"
	request := signedInternalRequest(t, now, "nonce-pet-photo-missing", http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d body=%s", response.Code, response.Body.String())
	}
	request = signedInternalRequest(t, now, "nonce-pet-photo-bound", http.MethodGet, path, nil)
	request.Header.Set("X-Device-Token", "pet-photo-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown photo status=%d body=%s", response.Code, response.Body.String())
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 4, 0, 0, 0xff, 0xc0, 0, 11, 8, 0, 180, 1, 64, 1, 1, 0x11, 0xff, 0xd9}
	digest := sha256.Sum256(jpeg)
	access, err := media.NewPetPhotoAccess(petPhotoHTTPRepo{photo: media.PetPhoto{
		ID: "018f0000-0000-7000-8000-000000000012", ObjectKey: "private/pet.jpg",
		SizeBytes: int64(len(jpeg)), SHA256: hex.EncodeToString(digest[:]),
	}}, petPhotoHTTPStore{bytes: jpeg})
	if err != nil {
		t.Fatal(err)
	}
	handler = newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService, PetPhotos: access})
	request = signedInternalRequest(t, now, "nonce-pet-photo-ready", http.MethodGet, path, nil)
	request.Header.Set("X-Device-Token", "pet-photo-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/jpeg" ||
		!bytes.Equal(response.Body.Bytes(), jpeg) || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private image status=%d headers=%v body=%x", response.Code, response.Header(), response.Body.Bytes())
	}
}
