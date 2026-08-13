package awsiot

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

func TestProvisionCreatesInactiveCertificateAndAttachesOnlyFixedResources(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	provisioner, err := New(client, "aws-iot-demo-", "aws-iot-demo-device")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	identity, err := provisioner.Provision(context.Background(), "activation-1", "csr-pem")
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if identity != (activation.PreparedIdentity{
		DeviceID:       "aws-iot-demo-activation-1",
		CertificateID:  "certificate-1",
		CertificatePEM: "certificate-pem",
		AWSIoTEndpoint: "endpoint-ats.iot.example",
	}) {
		t.Fatalf("Provision() = %#v", identity)
	}
	if client.certificateInput == nil || client.certificateInput.SetAsActive {
		t.Fatalf("certificate input = %#v, want inactive certificate", client.certificateInput)
	}
	if client.thingInput == nil || aws.ToString(client.thingInput.ThingName) != "aws-iot-demo-activation-1" {
		t.Fatalf("thing input = %#v", client.thingInput)
	}
	if client.policyInput == nil || aws.ToString(client.policyInput.PolicyName) != "aws-iot-demo-device" {
		t.Fatalf("policy input = %#v", client.policyInput)
	}
	if err := provisioner.Activate(context.Background(), identity.CertificateID); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if client.activateInput == nil || aws.ToString(client.activateInput.CertificateId) != "certificate-1" ||
		client.activateInput.NewStatus != "ACTIVE" {
		t.Fatalf("activate input = %#v", client.activateInput)
	}
}

type fakeClient struct {
	certificateInput *iot.CreateCertificateFromCsrInput
	thingInput       *iot.CreateThingInput
	policyInput      *iot.AttachPolicyInput
	activateInput    *iot.UpdateCertificateInput
}

func (f *fakeClient) CreateCertificateFromCsr(_ context.Context, input *iot.CreateCertificateFromCsrInput, _ ...func(*iot.Options)) (*iot.CreateCertificateFromCsrOutput, error) {
	f.certificateInput = input
	return &iot.CreateCertificateFromCsrOutput{
		CertificateArn: aws.String("certificate-arn"),
		CertificateId:  aws.String("certificate-1"),
		CertificatePem: aws.String("certificate-pem"),
	}, nil
}

func (f *fakeClient) CreateThing(_ context.Context, input *iot.CreateThingInput, _ ...func(*iot.Options)) (*iot.CreateThingOutput, error) {
	f.thingInput = input
	return &iot.CreateThingOutput{ThingName: input.ThingName}, nil
}

func (f *fakeClient) AttachThingPrincipal(context.Context, *iot.AttachThingPrincipalInput, ...func(*iot.Options)) (*iot.AttachThingPrincipalOutput, error) {
	return &iot.AttachThingPrincipalOutput{}, nil
}

func (f *fakeClient) AttachPolicy(_ context.Context, input *iot.AttachPolicyInput, _ ...func(*iot.Options)) (*iot.AttachPolicyOutput, error) {
	f.policyInput = input
	return &iot.AttachPolicyOutput{}, nil
}

func (f *fakeClient) UpdateCertificate(_ context.Context, input *iot.UpdateCertificateInput, _ ...func(*iot.Options)) (*iot.UpdateCertificateOutput, error) {
	f.activateInput = input
	return &iot.UpdateCertificateOutput{}, nil
}

func (f *fakeClient) DescribeEndpoint(context.Context, *iot.DescribeEndpointInput, ...func(*iot.Options)) (*iot.DescribeEndpointOutput, error) {
	return &iot.DescribeEndpointOutput{EndpointAddress: aws.String("endpoint-ats.iot.example")}, nil
}
