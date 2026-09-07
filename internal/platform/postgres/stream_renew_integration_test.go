package postgres

import (
	"context"
	"errors"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
	"testing"
	"time"
)

func TestStreamRenewalKeepsOwnershipAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	const deviceID = "018f0000-0000-7000-8000-000000000001"
	const bindingID = "018f0000-0000-7000-8000-000000000002"
	const streamID = "018f0000-0000-7000-8000-000000000003"
	const sn = "fdr-00000000000000000000000000000001"
	_, err := pool.Exec(ctx, `
 INSERT INTO device_registry(dp_profile_id, id,device_sn,thing_name,pid,status) VALUES('40000000-0000-0000-0000-000000000001', $1,$2,$2,'feeder-x1','active')`, deviceID, sn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO device_bindings(id,device_registry_id,internal_app_id,binding_external_user_id,status) VALUES($1,$2,'test-app','test-user','active')`, bindingID, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := NewStreamRepository(pool)
	deadline := time.Now().UTC().Add(4 * time.Minute)
	if err := repository.Create(ctx, media.Stream{ID: streamID, DeviceID: deviceID, DeviceSN: sn, BindingID: bindingID, ChannelARN: "session-test", Status: "ready", ExpiresAt: deadline.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	renewed, err := repository.Renew(ctx, bindingID, sn, streamID, deadline)
	if err != nil || renewed.Sub(deadline).Abs() > time.Microsecond {
		t.Fatalf("renew=%v %v", renewed, err)
	}
	renewed, err = repository.Renew(ctx, bindingID, sn, streamID, deadline.Add(-time.Minute))
	if err != nil || renewed.Sub(deadline).Abs() > time.Microsecond {
		t.Fatal("out-of-order renewal shortened lease")
	}
	for _, scenario := range []string{"other-device", "other-binding", "closed", "expired", "overlong", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `UPDATE device_streams SET status='ready',expires_at=now()+interval '1 minute' WHERE id=$1`, streamID); err != nil {
				t.Fatal(err)
			}
			targetSN, targetBinding := sn, bindingID
			targetDeadline := deadline
			switch scenario {
			case "other-device":
				targetSN = "fdr-other"
			case "other-binding":
				targetBinding = deviceID
			case "closed":
				_, err = pool.Exec(ctx, `UPDATE device_streams SET status='closed' WHERE id=$1`, streamID)
			case "expired":
				_, err = pool.Exec(ctx, `UPDATE device_streams SET expires_at=now()-interval '1 second' WHERE id=$1`, streamID)
			case "revoked":
				_, err = pool.Exec(ctx, `UPDATE device_bindings SET status='inactive',released_at=now() WHERE id=$1`, bindingID)
			case "overlong":
				targetDeadline = time.Now().Add(time.Hour)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.Renew(ctx, targetBinding, targetSN, streamID, targetDeadline); !errors.Is(err, media.ErrStreamNotFound) {
				t.Fatalf("%s renew=%v", scenario, err)
			}
		})
	}
}
