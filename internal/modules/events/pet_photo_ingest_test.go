package events

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type petPhotoWorkflowStub struct {
	ready, final int
	fail         bool
}

func (workflow *petPhotoWorkflowStub) CaptureReady(_ context.Context, event Event) error {
	workflow.ready++
	if workflow.fail {
		return errors.New("reject capture")
	}
	return nil
}

func (workflow *petPhotoWorkflowStub) VerifyFinal(_ context.Context, event Event) error {
	workflow.final++
	if workflow.fail {
		return errors.New("reject final")
	}
	return nil
}

func TestPetPhotoEventsFailClosedBeforeUserVisibleStorage(t *testing.T) {
	const ready = `{"topic":"dt/aws-iot-gateway-staging/fdr-0001/event","event_id":"018f0000-0000-7000-8000-000000000011","event_type":"pet_photo_ready_v1","boot_id":"5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148","event_seq":11,"data":{"trigger_id":"018f0000-0000-7000-8000-000000000012","model_id":"0x0A00002C","size_bytes":100,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
	const final = `{"topic":"dt/aws-iot-gateway-staging/fdr-0001/event","event_id":"018f0000-0000-7000-8000-000000000013","event_type":"pet_detected","boot_id":"5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148","event_seq":12,"data":{"schema":"pet-detection.v1","trigger_id":"018f0000-0000-7000-8000-000000000012","media_id":"018f0000-0000-7000-8000-000000000014","model_id":"0x0A00002C","image_url":"https://gateway.example.test/api/v1/openapi/devices/fdr-0001/pet-photos/018f0000-0000-7000-8000-000000000014"}}`
	const legacy = `{"topic":"dt/aws-iot-gateway-staging/fdr-0001/event","event_id":"018f0000-0000-7000-8000-000000000015","event_type":"pet_detected","boot_id":"5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148","event_seq":13,"data":{"score":99}}`
	oversizedReady := strings.Replace(ready, `"size_bytes":100`, `"size_bytes":262145`, 1)
	oversizedRepo := &eventRepositoryStub{}
	oversizedService, _ := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, oversizedRepo)
	oversizedService.SetPetPhotoWorkflow(&petPhotoWorkflowStub{})
	if err := oversizedService.Handle(context.Background(), []byte(oversizedReady)); err == nil || oversizedRepo.event.ID != "" {
		t.Fatal("oversized JPEG readiness accepted")
	}
	// New photo events fail closed; the existing score-only legacy event remains compatible.
	for _, payload := range []string{ready, final} {
		repo := &eventRepositoryStub{}
		svc, err := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Handle(context.Background(), []byte(payload)); err == nil {
			t.Fatalf("special pet photo event must fail closed without workflow: %s", payload)
		}
		if repo.event.ID != "" {
			t.Fatalf("event leaked into user list: %#v", repo.event)
		}
	}
	forged := strings.Replace(final, `"schema":"pet-detection.v1",`, "", 1)
	forgedRepo := &eventRepositoryStub{}
	forgedService, _ := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, forgedRepo)
	if err := forgedService.Handle(context.Background(), []byte(forged)); err == nil || forgedRepo.event.ID != "" {
		t.Fatal("dropping schema must not bypass private media validation")
	}
	legacyRepo := &eventRepositoryStub{}
	legacyService, _ := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, legacyRepo)
	if err := legacyService.Handle(context.Background(), []byte(legacy)); err != nil || legacyRepo.event.ID == "" {
		t.Fatalf("legacy pet event compatibility lost: err=%v event=%#v", err, legacyRepo.event)
	}
	repo := &eventRepositoryStub{}
	svc, _ := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, repo)
	workflow := &petPhotoWorkflowStub{}
	svc.SetPetPhotoWorkflow(workflow)
	if err := svc.Handle(context.Background(), []byte(ready)); err != nil {
		t.Fatal(err)
	}
	if workflow.ready != 1 || repo.event.ID != "" {
		t.Fatalf("ready event exposed: %#v", repo.event)
	}
	if err := svc.Handle(context.Background(), []byte(final)); err != nil {
		t.Fatal(err)
	}
	if workflow.final != 1 || repo.event.EventType != "pet_detected" {
		t.Fatalf("final event not verified/stored: %#v", repo.event)
	}
	for _, badURL := range []string{
		"https://gateway.example.test/api/v1/openapi/devices/other-device/pet-photos/018f0000-0000-7000-8000-000000000014",
		"https://gateway.example.test/api/v1/openapi/devices/fdr-0001/pet-photos/018f0000-0000-7000-8000-000000000099",
		"https://gateway.example.test/api/v1/openapi/devices/fdr-0001/pet-photos/018f0000-0000-7000-8000-000000000014?token=secret",
	} {
		tampered := strings.Replace(final, "https://gateway.example.test/api/v1/openapi/devices/fdr-0001/pet-photos/018f0000-0000-7000-8000-000000000014", badURL, 1)
		repo.event = Event{}
		if err := svc.Handle(context.Background(), []byte(tampered)); err == nil || repo.event.ID != "" {
			t.Fatalf("tampered URL stored: %s", badURL)
		}
	}
	repo.event = Event{}
	workflow.fail = true
	if err := svc.Handle(context.Background(), []byte(final)); err == nil {
		t.Fatal("rejected media event stored")
	}
	if repo.event.ID != "" {
		t.Fatal("rejected media event leaked")
	}
}
