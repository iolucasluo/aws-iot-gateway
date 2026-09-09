package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

func main() {
	os.Exit(run(os.Getenv, os.Stdout, os.Stderr))
}

func run(getenv func(string) string, stdout, stderr io.Writer) int {
	composition, err := gatewayruntime.LoadProviderComposition(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "provider composition preflight: FAIL: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprint(stdout, formatSummary(composition))
	return 0
}

func formatSummary(composition provider.Composition) string {
	instanceIDs := make([]string, 0, len(composition.Instances))
	for instanceID := range composition.Instances {
		instanceIDs = append(instanceIDs, instanceID)
	}
	sort.Strings(instanceIDs)

	var builder strings.Builder
	fmt.Fprintf(&builder, "provider composition preflight: OK\n")
	fmt.Fprintf(&builder, "id=%s environment=%s region=%s\n",
		composition.Metadata.ID, composition.Metadata.Environment, composition.Metadata.Region)
	for _, instanceID := range instanceIDs {
		instance := composition.Instances[instanceID]
		fmt.Fprintf(&builder, "instance=%s capability=%s driver=%s\n",
			instanceID, instance.Capability, instance.Driver)
	}
	return builder.String()
}
