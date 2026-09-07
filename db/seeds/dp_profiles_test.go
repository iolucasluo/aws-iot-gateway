package seeds

import (
	"strings"
	"testing"
)

func TestPublishedDPProfilesDefinesMinionCameraContract(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_dp_profiles.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		"INSERT INTO dp_definitions (id, code, type, direction, spec)",
		"'camera-v1'",
		"'video_res'",
		"'privacy_mode'",
		"INSERT INTO dp_profile_members",
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product DP contract missing %q", fragment)
		}
	}
}

func TestPublishedDPProfilesDefinesFeederContinuousRecordingDurations(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_dp_profiles.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		"'feeder-v1', 'record_mode'",
		`"segment_s":{"type":"integer","enum":[60,180,300]}`,
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product DP contract missing continuous recording contract %q", fragment)
		}
	}
}

func TestPublishedDPProfilesDefinesMachineCheckableDomains(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_dp_profiles.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		`"minimum":0,"maximum":100`,
		`"enum":["runtime","all"]`,
		`"enum":["start","stop"]`,
		`"ack":{"files":"finalized recording metadata objects"`,
		`"report_interval_semantics":"accepted for cross-repo compatibility but not applied by current firmware action"`,
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product DP contract missing machine-checkable domain %q", fragment)
		}
	}
}

func TestPublishedDPProfilesDefinesConditionalAndNestedSchemas(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_dp_profiles.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		`"variants":{"field":"op","cases":{"move"`,
		`"variants":{"field":"mode","cases":{"0"`,
		`"variants":{"field":"start","cases":{"1"`,
		`"items":{"type":"string","enum":["explorex.control.v1"]}`,
		`"constraints":{"positive":["width","height"],"max_sums":{"x+width":10000,"y+height":10000}}`,
		`"op":{"type":"string","enum":["list"]}`,
		`"date":{"type":"string"}`,
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product DP contract missing conditional/nested schema %q", fragment)
		}
	}
}

func TestPublishedDPProfilesDefinesFeederCommandAndMediaContract(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_dp_profiles.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, code := range []string{
		"clear_jam", "get_schedule", "get_feed_log", "device_info", "sdcard_status",
		"sdcard", "night_mode", "speaker_vol", "audio_playback", "motion_det",
		"ptz", "reboot", "two_way_audio", "video_res", "record_start", "record_stop",
		"stream", "snapshot", "record_clip", "audio_update", "audio_reset", "sd_files",
	} {
		fragment := "'feeder-v1', '" + code + "'"
		if !strings.Contains(seed, fragment) {
			t.Errorf("feeder DP contract missing %q", code)
		}
	}
}
