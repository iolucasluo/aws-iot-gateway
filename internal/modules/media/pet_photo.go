package media

import (
	"context"
	"errors"
	"time"
)

var ErrPetPhotoTaskNotFound = errors.New("pet photo task not found")

// PetPhotoTask is a private, binding-scoped upload transaction. It is never
// returned by the public event list until its JPEG has been verified.
type PetPhotoTask struct {
	ID, DeviceID, DeviceSN, InternalAppID string
	SHA256                                string
	SizeBytes                             int64
	Status, ObjectKey, ImageURL           string
	Attempts                              int
	Expired                               bool
	Revoked                               bool
}

type PetPhotoRepository interface {
	ClaimPetPhoto(context.Context) (PetPhotoTask, error)
	SetPetPhotoUploading(context.Context, string, string) error
	SetPetPhotoVerified(context.Context, string, string, string) error
	SetPetPhotoCleared(context.Context, string) error
	SetPetPhotoDead(context.Context, string) error
	SetPetPhotoExpired(context.Context, string) error
	SetPetPhotoExpiring(context.Context, string) error
	RetryPetPhoto(context.Context, string, string) error
	PurgePetPhotoTombstones(context.Context, time.Time, int) (int, error)
}

var (
	ErrPetPhotoNotFound    = errors.New("pet photo not found")
	ErrPetPhotoUnavailable = errors.New("pet photo temporarily unavailable")
)

type PetPhoto struct {
	ID, ObjectKey, SHA256 string
	SizeBytes             int64
}

type PetPhotoReadRepository interface {
	GetPetPhoto(context.Context, string, string, string) (PetPhoto, error)
}

type PetPhotoReadStore interface {
	Download(context.Context, string, int64) ([]byte, error)
}

type PetPhotoAccess struct {
	repository PetPhotoReadRepository
	store      PetPhotoReadStore
}

func NewPetPhotoAccess(repository PetPhotoReadRepository, store PetPhotoReadStore) (*PetPhotoAccess, error) {
	if repository == nil || store == nil {
		return nil, ErrPetPhotoUnavailable
	}
	return &PetPhotoAccess{repository: repository, store: store}, nil
}

func (access *PetPhotoAccess) Read(ctx context.Context, bindingID, deviceSN, mediaID string) ([]byte, error) {
	if access == nil || access.repository == nil || access.store == nil || bindingID == "" || deviceSN == "" || mediaID == "" {
		return nil, ErrPetPhotoNotFound
	}
	photo, err := access.repository.GetPetPhoto(ctx, bindingID, deviceSN, mediaID)
	if err != nil {
		return nil, err
	}
	if photo.ObjectKey == "" || photo.SizeBytes <= 0 || photo.SizeBytes > 256*1024 || len(photo.SHA256) != 64 {
		return nil, ErrPetPhotoUnavailable
	}
	payload, err := access.store.Download(ctx, photo.ObjectKey, 256*1024)
	if err != nil || int64(len(payload)) != photo.SizeBytes || !ValidateLocalThumbnailJPEG(payload, "sha256:"+photo.SHA256) {
		return nil, ErrPetPhotoUnavailable
	}
	return payload, nil
}
