package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

func main() {
	os.Exit(run(os.Getenv, os.Stdout, os.Stderr))
}

func run(getenv func(string) string, stdout, stderr io.Writer) int {
	composition, err := gatewayruntime.LoadProviderComposition(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "provider composition runtime audit: FAIL: %v\n", err)
		return 1
	}
	plan, err := gatewayruntime.BuildDomesticRuntimePlan(composition)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "provider composition runtime audit: FAIL: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprint(stdout, formatPlan(plan))
	if !plan.Ready {
		return 2
	}
	return 0
}

func formatPlan(plan gatewayruntime.DomesticRuntimePlan) string {
	status := "READY"
	if !plan.Ready {
		status = "BLOCKED"
	}

	var output strings.Builder
	fmt.Fprintf(&output, "provider composition runtime audit: %s\n", status)
	fmt.Fprintf(&output, "id=%s environment=%s region=%s\n", plan.CompositionID, plan.Environment, plan.Region)
	for _, binding := range plan.Bindings {
		bindingStatus := "SUPPORTED"
		if !binding.Supported {
			bindingStatus = "BLOCKED"
		}
		fmt.Fprintf(&output,
			"binding=%s instance=%s capability=%s driver=%s status=%s reason=%s\n",
			binding.Binding, binding.InstanceID, binding.Capability, binding.Driver, bindingStatus, binding.Reason,
		)
	}
	return output.String()
}
