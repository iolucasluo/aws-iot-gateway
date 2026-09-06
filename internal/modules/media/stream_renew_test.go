package media

import (
	"context"
	"errors"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
	"testing"
	"time"
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
			service := NewRealtimeStreamService(realtime, "realtime.test", &commanderStub{}, repository, nil)
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
