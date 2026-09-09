package media

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type renewableRealtimeStub struct {
	realtimeStub
	calls     int
	err       error
	overgrant bool
}

func (s *renewableRealtimeStub) RenewSession(_ context.Context, _ provider.RealtimeSessionRef, deadline time.Time) (time.Time, error) {
	s.calls++
	if s.overgrant {
		deadline = deadline.Add(time.Minute)
	}
	return deadline, s.err
}

type leaseRepositoryStub struct {
	streamRepositoryStub
	err error
}

func (r *leaseRepositoryStub) Renew(_ context.Context, _, _, _ string, deadline time.Time) (time.Time, error) {
	return deadline, r.err
}

func TestRenewStreamOwnershipAndPartialFailure(t *testing.T) {
	for _, scenario := range []string{"success", "other-binding", "other-device", "other-stream", "provider-failure", "revoked-binding", "overgrant", "aws"} {
		t.Run(scenario, func(t *testing.T) {
			realtime := &renewableRealtimeStub{}
			repository := &leaseRepositoryStub{streamRepositoryStub: streamRepositoryStub{stream: Stream{ID: "s1", BindingID: "b1", DeviceSN: "d1"}}}
			service := NewRealtimeStreamService(realtime, "realtime.test", &commanderStub{}, repository, nil, nil)
			binding, sn, id := "b1", "d1", "s1"
			switch scenario {
			case "other-binding":
				binding = "b2"
			case "other-device":
				sn = "d2"
			case "other-stream":
				id = "s2"
			case "provider-failure":
				realtime.err = errors.New("provider failed")
			case "revoked-binding":
				repository.err = ErrStreamNotFound
			case "overgrant":
				realtime.overgrant = true
			case "aws":
				service = NewStreamService(kvsStub{}, &commanderStub{}, repository, "prefix", nil)
			}
			lease, err := service.Renew(context.Background(), binding, sn, id)
			if scenario == "success" {
				if err != nil || !lease.ExpiresAt.After(time.Now()) || len(realtime.closed) != 0 {
					t.Fatalf("renew result=%+v err=%v", lease, err)
				}
			} else if err == nil {
				t.Fatal("invalid renewal succeeded")
			}
			if scenario == "other-binding" || scenario == "other-device" || scenario == "other-stream" || scenario == "aws" {
				if realtime.calls != 0 {
					t.Fatal("unauthorized/unsupported renewal reached provider")
				}
			}
			if scenario == "revoked-binding" || scenario == "overgrant" {
				if len(realtime.closed) != 1 {
					t.Fatal("provider lease left open after failed persistence/validation")
				}
			}
		})
	}
}

// TestRenewStreamObservabilityLog pins the one-line-per-outcome renewal log:
// every Renew outcome must identify the stream and, on failure, the reason and
// whether the provider session was closed. This is the evidence trail for
// diagnosing a transient session close that otherwise leaves no trace.
func TestRenewStreamObservabilityLog(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		config  func(realtime *renewableRealtimeStub, repository *leaseRepositoryStub)
		wantLog []string
	}{
		{
			name: "success",
			config: func(_ *renewableRealtimeStub, _ *leaseRepositoryStub) {},
			wantLog: []string{
				"renew_ok stream=s1 binding=b1 device=d1 expires_at=",
			},
		},
		{
			name: "provider-failure-keeps-session",
			config: func(realtime *renewableRealtimeStub, _ *leaseRepositoryStub) {
				realtime.err = errors.New("provider failed")
			},
			wantLog: []string{
				"renew_failed stream=s1 binding=b1 device=d1 reason=provider failed",
				"provider_session=kept",
			},
		},
		{
			name: "unknown-stream-keeps-session",
			config: func(_ *renewableRealtimeStub, repository *leaseRepositoryStub) {
				repository.stream = Stream{} // Find returns false
			},
			wantLog: []string{
				"renew_failed stream=s1 binding=b1 device=d1 reason=stream not found",
				"provider_session=kept",
			},
		},
		{
			name: "persistence-failure-closes-session",
			config: func(_ *renewableRealtimeStub, repository *leaseRepositoryStub) {
				repository.err = ErrStreamNotFound
			},
			wantLog: []string{
				"renew_failed stream=s1 binding=b1 device=d1 reason=stream not found",
				"provider_session=closed",
			},
		},
		{
			name: "overgrant-closes-session",
			config: func(realtime *renewableRealtimeStub, _ *leaseRepositoryStub) {
				realtime.overgrant = true
			},
			wantLog: []string{
				"renew_failed stream=s1 binding=b1 device=d1",
				"provider_session=closed",
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			realtime := &renewableRealtimeStub{}
			repository := &leaseRepositoryStub{streamRepositoryStub: streamRepositoryStub{stream: Stream{ID: "s1", BindingID: "b1", DeviceSN: "d1"}}}
			scenario.config(realtime, repository)
			var captured bytes.Buffer
			service := NewRealtimeStreamService(realtime, "realtime.test", &commanderStub{}, repository, nil, log.New(&captured, "", 0))
			lease, err := service.Renew(context.Background(), "b1", "d1", "s1")
			if scenario.name == "success" {
				if err != nil || !lease.ExpiresAt.After(time.Now()) {
					t.Fatalf("renew result=%+v err=%v", lease, err)
				}
			} else if err == nil {
				t.Fatal("invalid renewal succeeded")
			}
			logs := captured.String()
			for _, want := range scenario.wantLog {
				if !strings.Contains(logs, want) {
					t.Fatalf("renew log missing %q; got:\n%s", want, logs)
				}
			}
		})
	}
}
