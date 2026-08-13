package awsiot

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"github.com/aws/aws-sdk-go-v2/service/iot/types"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

type Client interface {
	CreateCertificateFromCsr(context.Context, *iot.CreateCertificateFromCsrInput, ...func(*iot.Options)) (*iot.CreateCertificateFromCsrOutput, error)
	CreateThing(context.Context, *iot.CreateThingInput, ...func(*iot.Options)) (*iot.CreateThingOutput, error)
	AttachThingPrincipal(context.Context, *iot.AttachThingPrincipalInput, ...func(*iot.Options)) (*iot.AttachThingPrincipalOutput, error)
	AttachPolicy(context.Context, *iot.AttachPolicyInput, ...func(*iot.Options)) (*iot.AttachPolicyOutput, error)
	UpdateCertificate(context.Context, *iot.UpdateCertificateInput, ...func(*iot.Options)) (*iot.UpdateCertificateOutput, error)
	DescribeEndpoint(context.Context, *iot.DescribeEndpointInput, ...func(*iot.Options)) (*iot.DescribeEndpointOutput, error)
}

type Provisioner struct {
	client      Client
	thingPrefix string
	policyName  string
}

func New(client Client, thingPrefix, policyName string) (*Provisioner, error) {
	if client == nil || thingPrefix == "" || policyName == "" {
		return nil, fmt.Errorf("AWS IoT provisioner requires client, thing prefix, and policy name")
	}
	return &Provisioner{client: client, thingPrefix: thingPrefix, policyName: policyName}, nil
}

func (p *Provisioner) Provision(ctx context.Context, activationID, csrPEM string) (activation.PreparedIdentity, error) {
	thingName := p.thingPrefix + activationID
	certificate, err := p.client.CreateCertificateFromCsr(ctx, &iot.CreateCertificateFromCsrInput{
		CertificateSigningRequest: aws.String(csrPEM),
		SetAsActive:               false,
	})
	if err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("create certificate from csr: %w", err)
	}
	if certificate.CertificateArn == nil || certificate.CertificateId == nil || certificate.CertificatePem == nil {
		return activation.PreparedIdentity{}, fmt.Errorf("create certificate from csr: incomplete response")
	}
	thing, err := p.client.CreateThing(ctx, &iot.CreateThingInput{ThingName: aws.String(thingName)})
	if err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("create thing: %w", err)
	}
	if thing.ThingName == nil || *thing.ThingName == "" {
		return activation.PreparedIdentity{}, fmt.Errorf("create thing: incomplete response")
	}
	if _, err := p.client.AttachThingPrincipal(ctx, &iot.AttachThingPrincipalInput{
		Principal:          certificate.CertificateArn,
		ThingName:          thing.ThingName,
		ThingPrincipalType: types.ThingPrincipalTypeExclusiveThing,
	}); err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("attach certificate to thing: %w", err)
	}
	if _, err := p.client.AttachPolicy(ctx, &iot.AttachPolicyInput{
		PolicyName: aws.String(p.policyName),
		Target:     certificate.CertificateArn,
	}); err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("attach device policy: %w", err)
	}
	endpoint, err := p.client.DescribeEndpoint(ctx, &iot.DescribeEndpointInput{EndpointType: aws.String("iot:Data-ATS")})
	if err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("describe data endpoint: %w", err)
	}
	if endpoint.EndpointAddress == nil || *endpoint.EndpointAddress == "" {
		return activation.PreparedIdentity{}, fmt.Errorf("describe data endpoint: incomplete response")
	}
	return activation.PreparedIdentity{
		DeviceID:       *thing.ThingName,
		CertificateID:  *certificate.CertificateId,
		CertificatePEM: *certificate.CertificatePem,
		AWSIoTEndpoint: *endpoint.EndpointAddress,
	}, nil
}

func (p *Provisioner) Activate(ctx context.Context, certificateID string) error {
	if certificateID == "" {
		return fmt.Errorf("certificate id is empty")
	}
	if _, err := p.client.UpdateCertificate(ctx, &iot.UpdateCertificateInput{
		CertificateId: aws.String(certificateID),
		NewStatus:     types.CertificateStatusActive,
	}); err != nil {
		return fmt.Errorf("activate certificate: %w", err)
	}
	return nil
}
