package runtime

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

// BindingRuntimeStatus is the non-sensitive result of checking one selected
// composition binding against the adapters that are actually wired into the
// domestic application runtime. It intentionally omits endpoint and secret
// references from the report.
type BindingRuntimeStatus struct {
	Binding    string
	InstanceID string
	Capability provider.Capability
	Driver     string
	Supported  bool
	Reason     string
}

// DomesticRuntimePlan is an offline, fail-closed plan for the domestic
// composition. It is an audit boundary, not an application server and does
// not create clients or perform network I/O.
type DomesticRuntimePlan struct {
	CompositionID string
	Environment   string
	Region        string
	Bindings      []BindingRuntimeStatus
	Ready         bool
}

// BuildDomesticRuntimePlan validates a composition and reports whether every
// explicitly selected capability has a domestic application adapter. The
// function must run before any SDK or network initialization. A valid plan can
// still be blocked when a provider port exists but its application wiring is
// not registered yet.
func BuildDomesticRuntimePlan(composition provider.Composition) (DomesticRuntimePlan, error) {
	if err := composition.Validate(); err != nil {
		return DomesticRuntimePlan{}, fmt.Errorf("validate domestic runtime composition: %w", err)
	}
	if !strings.HasPrefix(composition.Metadata.Region, "cn") {
		return DomesticRuntimePlan{}, fmt.Errorf("domestic runtime audit requires a cn region")
	}

	bindingNames := make([]string, 0, len(composition.Bindings))
	for binding := range composition.Bindings {
		bindingNames = append(bindingNames, binding)
	}
	sort.Strings(bindingNames)

	plan := DomesticRuntimePlan{
		CompositionID: composition.Metadata.ID,
		Environment:   composition.Metadata.Environment,
		Region:        composition.Metadata.Region,
		Bindings:      make([]BindingRuntimeStatus, 0, len(bindingNames)),
		Ready:         true,
	}
	for _, binding := range bindingNames {
		instanceID := composition.Bindings[binding]
		instance := composition.Instances[instanceID]
		supported, reason := domesticAdapterSupport(instance.Capability, instance.Driver)
		plan.Bindings = append(plan.Bindings, BindingRuntimeStatus{
			Binding: binding, InstanceID: instanceID, Capability: instance.Capability,
			Driver: instance.Driver, Supported: supported, Reason: reason,
		})
		if !supported {
			plan.Ready = false
		}
	}
	return plan, nil
}

func domesticAdapterSupport(capability provider.Capability, driver string) (bool, string) {
	if capability == provider.CapabilityMessaging && driver == "mqtt_broker" {
		return true, "registered adapter: mqtt_broker"
	}
	if driver == "explorex_signal" {
		return false, "provider adapter exists; application runtime wiring is pending"
	}
	return false, fmt.Sprintf("adapter not registered: %s/%s", capability, driver)
}
