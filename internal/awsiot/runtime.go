package awsiot

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iotdataplane"
)

type DataClient interface {
	Publish(context.Context, *iotdataplane.PublishInput, ...func(*iotdataplane.Options)) (*iotdataplane.PublishOutput, error)
	GetThingShadow(context.Context, *iotdataplane.GetThingShadowInput, ...func(*iotdataplane.Options)) (*iotdataplane.GetThingShadowOutput, error)
}

type Runtime struct{ client DataClient }

func NewRuntime(client DataClient) (*Runtime, error) {
	if client == nil {
		return nil, fmt.Errorf("AWS IoT runtime requires a data-plane client")
	}
	return &Runtime{client: client}, nil
}

func (runtime *Runtime) Publish(ctx context.Context, topic string, payload []byte) error {
	if topic == "" {
		return fmt.Errorf("AWS IoT publish topic is empty")
	}
	if _, err := runtime.client.Publish(ctx, &iotdataplane.PublishInput{
		Topic: aws.String(topic), Payload: payload, Qos: 1, ContentType: aws.String("application/json"),
	}); err != nil {
		return fmt.Errorf("publish AWS IoT message: %w", err)
	}
	return nil
}

func (runtime *Runtime) GetShadow(ctx context.Context, deviceSN string) ([]byte, error) {
	if deviceSN == "" {
		return nil, fmt.Errorf("AWS IoT shadow thing name is empty")
	}
	result, err := runtime.client.GetThingShadow(ctx, &iotdataplane.GetThingShadowInput{ThingName: aws.String(deviceSN)})
	if err != nil {
		return nil, fmt.Errorf("get AWS IoT thing shadow: %w", err)
	}
	return append([]byte(nil), result.Payload...), nil
}
