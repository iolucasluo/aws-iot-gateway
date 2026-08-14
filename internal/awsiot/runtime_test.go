package awsiot

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iotdataplane"
)

func TestRuntimeReadsTheConfiguredNamedShadow(t *testing.T) {
	t.Parallel()

	client := &runtimeDataClient{}
	runtime, err := NewRuntime(client, "internal-aws")
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	payload, err := runtime.GetShadow(context.Background(), "fdr-00000000000000000000000000000001")
	if err != nil {
		t.Fatalf("GetShadow() error = %v", err)
	}
	if string(payload) != `{"state":{"reported":{"online":true}}}` {
		t.Fatalf("GetShadow() payload = %s", payload)
	}
	if client.shadowInput == nil ||
		aws.ToString(client.shadowInput.ThingName) != "fdr-00000000000000000000000000000001" ||
		aws.ToString(client.shadowInput.ShadowName) != "internal-aws" {
		t.Fatalf("GetThingShadow() input = %#v", client.shadowInput)
	}
}

type runtimeDataClient struct {
	shadowInput *iotdataplane.GetThingShadowInput
}

func (client *runtimeDataClient) Publish(context.Context, *iotdataplane.PublishInput, ...func(*iotdataplane.Options)) (*iotdataplane.PublishOutput, error) {
	return &iotdataplane.PublishOutput{}, nil
}

func (client *runtimeDataClient) GetThingShadow(_ context.Context, input *iotdataplane.GetThingShadowInput, _ ...func(*iotdataplane.Options)) (*iotdataplane.GetThingShadowOutput, error) {
	client.shadowInput = input
	return &iotdataplane.GetThingShadowOutput{Payload: []byte(`{"state":{"reported":{"online":true}}}`)}, nil
}
