package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

var petJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 4, 0, 0, 0xff, 0xc0, 0, 11, 8, 0, 180, 1, 64, 1, 1, 0x11, 0xff, 0xd9}

type petPhotoReadRepoStub struct {
	photo     PetPhoto
	bindingID string
}

func (repo *petPhotoReadRepoStub) GetPetPhoto(_ context.Context, bindingID, _, _ string) (PetPhoto, error) {
	repo.bindingID = bindingID
	if repo.photo.ID == "" {
		return PetPhoto{}, ErrPetPhotoNotFound
	}
	return repo.photo, nil
}

type petPhotoReadStoreStub struct{ payload []byte }

func (store petPhotoReadStoreStub) Download(context.Context, string, int64) ([]byte, error) {
	return store.payload, nil
}

func TestPetPhotoAccessRequiresBoundVerifiedJPEG(t *testing.T) {
	digest := sha256.Sum256(petJPEG)
	repo := &petPhotoReadRepoStub{photo: PetPhoto{ID: "media-1", ObjectKey: "private/pet.jpg", SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(petJPEG))}}
	access, err := NewPetPhotoAccess(repo, petPhotoReadStoreStub{payload: petJPEG})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := access.Read(context.Background(), "binding-1", "fdr-0001", "media-1")
	if err != nil || string(payload) != string(petJPEG) || repo.bindingID != "binding-1" {
		t.Fatalf("payload=%x err=%v binding=%q", payload, err, repo.bindingID)
	}
	access, _ = NewPetPhotoAccess(repo, petPhotoReadStoreStub{payload: []byte("not jpeg")})
	if _, err := access.Read(context.Background(), "binding-1", "fdr-0001", "media-1"); !errors.Is(err, ErrPetPhotoUnavailable) {
		t.Fatalf("invalid private object err=%v", err)
	}
	if _, err := access.Read(context.Background(), "", "fdr-0001", "media-1"); err == nil {
		t.Fatal("empty binding accepted")
	}
}
