package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestRecorder(t *testing.T) (*Recorder, *MemoryRepository) {
	t.Helper()
	repository := NewMemoryRepository()
	sequence := 0
	recorder, err := NewRecorder(repository, func() time.Time {
		return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	}, func() (string, error) {
		sequence++
		return "audit-entry-" + string(rune('0'+sequence)), nil
	})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	return recorder, repository
}

func TestRecorderNormalizesAndPersistsEntry(t *testing.T) {
	t.Parallel()
	recorder, repository := newTestRecorder(t)
	err := recorder.Record(context.Background(), Entry{
		ActorKind:      ActorPortalAccount,
		ActorAccountID: "10000000-0000-0000-0000-000000000001",
		ActorLabel:     " ops@example.com ",
		Operation:      " auth.login ",
		Outcome:        OutcomeSucceeded,
		Metadata:       map[string]any{"status": 200},
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	entries := repository.Entries()
	if len(entries) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Operation != "auth.login" || entry.ActorLabel != "ops@example.com" {
		t.Fatalf("entry was not normalised: %+v", entry)
	}
	if entry.ID == "" || entry.CreatedAt.IsZero() {
		t.Fatalf("entry identity/time was not filled: %+v", entry)
	}
}

func TestRecorderRejectsInvalidEntries(t *testing.T) {
	t.Parallel()
	recorder, repository := newTestRecorder(t)
	for name, entry := range map[string]Entry{
		"missing-operation": {ActorKind: ActorSystem, Outcome: OutcomeSucceeded},
		"unknown-actor":     {ActorKind: "robot", Operation: "x.y", Outcome: OutcomeSucceeded},
		"unknown-outcome":   {ActorKind: ActorSystem, Operation: "x.y", Outcome: "maybe"},
		"anonymous-portal":  {ActorKind: ActorPortalAccount, Operation: "x.y", Outcome: OutcomeSucceeded},
	} {
		if err := recorder.Record(context.Background(), entry); !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("Record(%s) error = %v, want ErrInvalidEntry", name, err)
		}
	}
	if len(repository.Entries()) != 0 {
		t.Fatal("invalid entries must not be persisted")
	}
}

func TestNewRecorderRequiresCollaborators(t *testing.T) {
	t.Parallel()
	if _, err := NewRecorder(nil, nil, nil); err == nil {
		t.Fatal("NewRecorder(nil) was accepted")
	}
	if _, err := NewRecorder(NewMemoryRepository(), nil, nil); err == nil {
		t.Fatal("NewRecorder without an id generator was accepted")
	}
}
