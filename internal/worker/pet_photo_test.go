package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type petPhotoRepoStub struct {
	task             media.PetPhotoTask
	status           string
	key, url, reason string
}

func (repo *petPhotoRepoStub) ClaimPetPhoto(context.Context) (media.PetPhotoTask, error) {
	return repo.task, nil
}
func (repo *petPhotoRepoStub) SetPetPhotoUploading(_ context.Context, _, key string) error {
	repo.status = "uploading"
	repo.key = key
	return nil
}
func (repo *petPhotoRepoStub) SetPetPhotoVerified(_ context.Context, _, key, url string) error {
	repo.status = "verified"
	repo.key = key
	repo.url = url
	return nil
}
func (repo *petPhotoRepoStub) SetPetPhotoDead(_ context.Context, _ string) error {
	repo.status = "dead"
	return nil
}
func (repo *petPhotoRepoStub) SetPetPhotoExpired(_ context.Context, _ string) error {
	repo.status = "expired"
	return nil
}
func (repo *petPhotoRepoStub) SetPetPhotoCleared(_ context.Context, _ string) error {
	repo.status = "cleared"
	return nil
}

func (repo *petPhotoRepoStub) RetryPetPhoto(_ context.Context, _, reason string) error {
	repo.reason = reason
	return nil
}

func TestPetPhotoWorkerReusesPrivateUploadAndOnlyCommitsVerifiedJPEG(t *testing.T) {
	digest := sha256.Sum256(jpeg320x180)
	id := "018f0000-0000-7000-8000-000000000012"
	task := media.PetPhotoTask{ID: id, DeviceID: "018f0000-0000-7000-8000-000000000010", DeviceSN: "fdr-0001", InternalAppID: "livpet-staging", SizeBytes: int64(len(jpeg320x180)), SHA256: hex.EncodeToString(digest[:]), Status: "pending"}
	repo := &petPhotoRepoStub{task: task}
	store := &localThumbnailStoreStub{payload: jpeg320x180, statSize: int64(len(jpeg320x180)), statType: "image/jpeg"}
	commander := &localThumbnailCommanderStub{}
	worker, err := NewPetPhotoWorker(repo, store, commander, "staging", "https://gateway.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.status != "uploading" || repo.key != store.putKey || store.putSize != task.SizeBytes {
		t.Fatalf("pending=%#v store=%#v", repo, store)
	}
	if commander.input.Code != "pet_photo_v1" || len(commander.input.RedactFields) != 1 {
		t.Fatalf("upload command=%#v", commander.input)
	}
	var upload map[string]any
	if err := json.Unmarshal(commander.input.Value, &upload); err != nil || upload["op"] != "upload" || upload["trigger_id"] != id {
		t.Fatalf("upload=%v err=%v", upload, err)
	}
	repo.task.Status = "uploading"
	repo.task.ObjectKey = repo.key
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.status != "verified" || repo.url != "https://gateway.example.test/api/v1/openapi/devices/fdr-0001/pet-photos/"+id {
		t.Fatalf("verified=%#v", repo)
	}
	repo.task.Status = "verified"
	repo.task.ObjectKey = repo.key
	repo.task.ImageURL = repo.url
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var commit map[string]any
	if err := json.Unmarshal(commander.input.Value, &commit); err != nil || commit["op"] != "commit" || commit["image_url"] != repo.url {
		t.Fatalf("commit=%v err=%v", commit, err)
	}
	repo.task.Status = "finalized"
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var cleared map[string]any
	if err := json.Unmarshal(commander.input.Value, &cleared); err != nil ||
		cleared["op"] != "ack" || repo.status != "cleared" {
		t.Fatalf("clear after event storage: command=%v repo=%#v err=%v", cleared, repo, err)
	}
	repo.task.Status = "uploading"
	repo.status = ""
	store.payload = []byte("not a jpeg")
	if err := worker.PollOnce(context.Background()); err == nil || repo.status == "verified" {
		t.Fatal("invalid image must not be newly verified")
	}
	repo.task.Status = "pending"
	repo.task.Expired = true
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatalf("cancel expired capture: %v", err)
	}
	var cancel map[string]any
	if err := json.Unmarshal(commander.input.Value, &cancel); err != nil || cancel["op"] != "cancel" || repo.status != "dead" {
		t.Fatalf("expired capture=%v status=%q err=%v", cancel, repo.status, err)
	}
	repo.task.Status = "cleared"
	repo.task.ObjectKey = repo.key
	if err := worker.PollOnce(context.Background()); err != nil || repo.status != "expired" || store.deletedKey != repo.key {
		t.Fatalf("expired resource cleanup err=%v repo=%#v store=%#v", err, repo, store)
	}
	repo.task.Status = "verified"
	repo.task.Revoked = true
	repo.task.Expired = false
	repo.task.ObjectKey = repo.key
	repo.status = ""
	store.deletedKey = ""
	if err := worker.PollOnce(context.Background()); err != nil || repo.status != "dead" || store.deletedKey != repo.key {
		t.Fatalf("revoked binding cleanup err=%v repo=%#v store=%#v", err, repo, store)
	}
}
