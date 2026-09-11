package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

func TestSeededProductProfilesMatchFirmwareBoundaries(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	repository, err := NewDPProfileRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ id, sn, pid, profile string }{
		{"30000000-0000-0000-0000-000000000001", "anyka-feeder", "feeder-x1", "feeder-v1"},
		{"30000000-0000-0000-0000-000000000002", "anyka-lamp", "lamp-x1", "lamp-v1"},
		{"30000000-0000-0000-0000-000000000003", "taixin-minion", "minion-cam-x1", "camera-v1"},
	} {
		insertProfileDevice(t, pool, fixture.id, fixture.sn, fixture.pid, fixture.profile)
	}
	for _, tc := range []struct {
		sn, code, value string
		want            error
	}{
		{"anyka-feeder", "feed", `{"portions":1}`, nil},
		{"anyka-feeder", "feed", `{"portions":0}`, device.ErrDPInvalidValue},
		{"anyka-feeder", "feed", `{"portions":11}`, device.ErrDPInvalidValue},
		{"anyka-feeder", "set_schedule", `{"schedules":[{"id":"sched_1","hour":8,"minute":0,"portions":2,"enabled":true}]}`, nil},
		{"anyka-lamp", "feed", `{"portions":1}`, device.ErrDPUnknown},
		{"anyka-lamp", "set_schedule", `{"schedules":[]}`, device.ErrDPUnknown},
		{"anyka-lamp", "audio_update", `{}`, device.ErrDPUnknown},
		{"anyka-lamp", "query_status", `{}`, nil},
		{"anyka-lamp", "motion_track", `{"enabled":true}`, nil},
		{"anyka-lamp", "video_res", `{"resolution":"1080p"}`, nil},
		{"taixin-minion", "video_res", `{"resolution":"1080p"}`, device.ErrDPInvalidValue},
		{"taixin-minion", "video_res", `{"resolution":"480p"}`, nil},
		{"taixin-minion", "record_mode", `{"mode":1,"segment_s":180}`, device.ErrDPInvalidValue},
		{"taixin-minion", "record_mode", `{"mode":1,"segment_s":60,"high_water_pct":85,"low_water_pct":75,"reserve_kb":131072}`, nil},
		{"taixin-minion", "record_mode", `{"high_water_pct":90}`, device.ErrDPInvalidValue},
		{"taixin-minion", "record_start", `{"mode":1,"duration_s":0}`, device.ErrDPInvalidValue},
		{"taixin-minion", "record_start", `{"mode":1,"duration_s":1}`, nil},
		{"anyka-feeder", "audio_playback", `{"enabled":false}`, nil},
		{"anyka-feeder", "feed_done", `{"portions":1,"trigger":"app"}`, device.ErrDPReadOnly},
		{"anyka-feeder", "ota_install", `{}`, device.ErrDPDisabled},
		{"anyka-lamp", "sd_recordings_v1", `{"op":"list"}`, device.ErrDPDisabled},
		{"anyka-lamp", "led", `{"state":1}`, nil},
		{"anyka-lamp", "led", `{"brightness":50}`, device.ErrDPInvalidValue},
		{"anyka-lamp", "sd_files", `{"op":"count"}`, nil},
		{"anyka-lamp", "sd_files", `{"op":"delete"}`, device.ErrDPInvalidValue},
		{"anyka-lamp", "recommission", `{"scope":"all"}`, device.ErrDPControlled},
	} {
		t.Run(tc.sn+"/"+tc.code+"/"+tc.value, func(t *testing.T) {
			definitions, err := repository.LoadDPDefinitions(ctx, tc.sn)
			if err != nil {
				t.Fatal(err)
			}
			err = device.ValidateCommands(definitions, []device.CommandInput{{Code: tc.code, Value: json.RawMessage(tc.value)}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("validation = %v, want %v", err, tc.want)
			}
		})
	}
	var shared bool
	if err := pool.QueryRow(ctx, `
		SELECT a.dp_definition_id=b.dp_definition_id
		FROM dp_profile_members a JOIN dp_profiles ap ON ap.code=a.dp_profile_code
		JOIN dp_profile_members b ON b.code=a.code
		JOIN dp_profiles bp ON bp.code=b.dp_profile_code
		WHERE ap.code='feeder-v1' AND bp.code='lamp-v1' AND a.code='night_mode'
	`).Scan(&shared); err != nil || !shared {
		t.Fatalf("identical Anyka contracts must share a definition: shared=%t error=%v", shared, err)
	}
}
