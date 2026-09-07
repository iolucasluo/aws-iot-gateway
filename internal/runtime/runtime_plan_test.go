package runtime

import (
	"strings"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func TestBuildDomesticRuntimePlanReportsBindingSupportDeterministically(t *testing.T) {
	t.Parallel()

	composition := provider.Composition{
		APIVersion: provider.APIVersion,
		Kind:       provider.Kind,
		Metadata: provider.Metadata{
			ID: "cn-staging", Environment: "staging", Region: "cn",
		},
		Instances: map[string]provider.Instance{
			"identity.cn_staging":  {Capability: provider.CapabilityIdentity, Driver: "ca_mqtt_identity"},
			"messaging.cn_staging": {Capability: provider.CapabilityMessaging, Driver: "mqtt_broker"},
			"signer.cn_staging":    {Capability: provider.CapabilitySigner, Driver: "domestic_kms"},
			"realtime.cn_staging":  {Capability: provider.CapabilityRealtime, Driver: "explorex_signal"},
			"media.cn_staging":     {Capability: provider.CapabilityObject, Driver: "aliyun_oss"},
			"artifact.cn_staging":  {Capability: provider.CapabilityArtifact, Driver: "tencent_cos"},
			"queue.cn_staging":     {Capability: provider.CapabilityQueue, Driver: "queue_broker"},
			"twin.cn_staging":      {Capability: provider.CapabilityTwin, Driver: "broker_twin"},
		},
		Bindings: map[string]string{
			"identity":     "identity.cn_staging",
			"messaging":    "messaging.cn_staging",
			"signer":       "signer.cn_staging",
			"realtime":     "realtime.cn_staging",
			"media_object": "media.cn_staging",
			"ota_artifact": "artifact.cn_staging",
			"queue":        "queue.cn_staging",
			"twin":         "twin.cn_staging",
		},
		Policy: provider.Policy{AllowMixedProvider: true},
	}

	plan, err := BuildDomesticRuntimePlan(composition)
	if err != nil {
		t.Fatalf("BuildDomesticRuntimePlan() error = %v", err)
	}
	if plan.Ready {
		t.Fatal("BuildDomesticRuntimePlan() Ready = true, want blocked until all bound adapters are wired")
	}
	if got, want := len(plan.Bindings), len(composition.Bindings); got != want {
		t.Fatalf("plan bindings = %d, want %d", got, want)
	}
	if got, want := plan.Bindings[0].Binding, "identity"; got != want {
		t.Fatalf("first binding = %q, want sorted binding %q", got, want)
	}
	statuses := make(map[string]BindingRuntimeStatus, len(plan.Bindings))
	for _, status := range plan.Bindings {
		statuses[status.Binding] = status
	}
	if status := statuses["messaging"]; !status.Supported || status.Reason != "registered adapter: mqtt_broker" {
		t.Fatalf("messaging status = %#v, want supported mqtt_broker adapter", status)
	}
	for _, binding := range []string{"identity", "signer", "realtime", "media_object", "ota_artifact", "queue", "twin"} {
		if statuses[binding].Supported {
			t.Fatalf("binding %q unexpectedly supported: %#v", binding, statuses[binding])
		}
		if strings.TrimSpace(statuses[binding].Reason) == "" {
			t.Fatalf("binding %q has empty blocker reason", binding)
		}
	}
}

func TestBuildDomesticRuntimePlanRejectsNonDomesticComposition(t *testing.T) {
	t.Parallel()

	composition := minimalDomesticComposition()
	composition.Metadata.Region = "us"
	if _, err := BuildDomesticRuntimePlan(composition); err == nil || !strings.Contains(err.Error(), "requires a cn region") {
		t.Fatalf("BuildDomesticRuntimePlan() error = %v, want domestic region rejection", err)
	}
}

func TestBuildDomesticRuntimePlanValidatesBeforePlanning(t *testing.T) {
	t.Parallel()

	composition := minimalDomesticComposition()
	composition.Bindings["messaging"] = "missing"
	if _, err := BuildDomesticRuntimePlan(composition); err == nil || !strings.Contains(err.Error(), "invalid provider composition") {
		t.Fatalf("BuildDomesticRuntimePlan() error = %v, want composition validation failure", err)
	}
}

func TestBuildDomesticRuntimePlanRejectsAWSDataPlaneBeforePlanning(t *testing.T) {
	t.Parallel()

	composition := minimalDomesticComposition()
	composition.Instances["messaging.cn_staging"] = provider.Instance{
		Capability: provider.CapabilityMessaging,
		Driver:     "aws_iot_mqtt",
	}
	if _, err := BuildDomesticRuntimePlan(composition); err == nil || !strings.Contains(err.Error(), "domestic composition cannot use AWS data-plane") {
		t.Fatalf("BuildDomesticRuntimePlan() error = %v, want AWS data-plane rejection", err)
	}
}

func minimalDomesticComposition() provider.Composition {
	return provider.Composition{
		APIVersion: provider.APIVersion,
		Kind:       provider.Kind,
		Metadata: provider.Metadata{
			ID: "cn-staging", Environment: "staging", Region: "cn",
		},
		Instances: map[string]provider.Instance{
			"identity.cn_staging":  {Capability: provider.CapabilityIdentity, Driver: "ca_mqtt_identity"},
			"messaging.cn_staging": {Capability: provider.CapabilityMessaging, Driver: "mqtt_broker"},
			"signer.cn_staging":    {Capability: provider.CapabilitySigner, Driver: "domestic_kms"},
		},
		Bindings: map[string]string{
			"identity": "identity.cn_staging", "messaging": "messaging.cn_staging", "signer": "signer.cn_staging",
		},
		Policy: provider.Policy{AllowMixedProvider: true},
	}
}
