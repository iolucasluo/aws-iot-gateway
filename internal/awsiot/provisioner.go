package awsiot

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"github.com/aws/aws-sdk-go-v2/service/iot/types"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

type Client interface {
	CreateCertificateFromCsr(context.Context, *iot.CreateCertificateFromCsrInput, ...func(*iot.Options)) (*iot.CreateCertificateFromCsrOutput, error)
	DescribeThing(context.Context, *iot.DescribeThingInput, ...func(*iot.Options)) (*iot.DescribeThingOutput, error)
	CreateThing(context.Context, *iot.CreateThingInput, ...func(*iot.Options)) (*iot.CreateThingOutput, error)
	AttachThingPrincipal(context.Context, *iot.AttachThingPrincipalInput, ...func(*iot.Options)) (*iot.AttachThingPrincipalOutput, error)
	AttachPolicy(context.Context, *iot.AttachPolicyInput, ...func(*iot.Options)) (*iot.AttachPolicyOutput, error)
	UpdateCertificate(context.Context, *iot.UpdateCertificateInput, ...func(*iot.Options)) (*iot.UpdateCertificateOutput, error)
	DescribeEndpoint(context.Context, *iot.DescribeEndpointInput, ...func(*iot.Options)) (*iot.DescribeEndpointOutput, error)
}

type Provisioner struct {
	client     Client
	policyName string
}

func New(client Client, policyName string) (*Provisioner, error) {
	if client == nil || policyName == "" {
		return nil, fmt.Errorf("AWS IoT provisioner requires client and policy name")
	}
	return &Provisioner{client: client, policyName: policyName}, nil
}

func (p *Provisioner) Provision(ctx context.Context, deviceSN, csrPEM string) (activation.PreparedIdentity, error) {
	if deviceSN == "" {
		return activation.PreparedIdentity{}, fmt.Errorf("device sn is empty")
	}
	thingName := deviceSN
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
	if err := p.ensureThing(ctx, thingName); err != nil {
		return activation.PreparedIdentity{}, err
	}
	if _, err := p.client.AttachThingPrincipal(ctx, &iot.AttachThingPrincipalInput{
		Principal:          certificate.CertificateArn,
		ThingName:          aws.String(thingName),
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
		DeviceID:       thingName,
		CertificateID:  *certificate.CertificateId,
		CertificatePEM: *certificate.CertificatePem,
		AWSIoTEndpoint: *endpoint.EndpointAddress,
	}, nil
}

// ensureThing 让 Thing 的创建幂等：设备重新激活时复用已存在的 Thing，
// 否则 CreateThing 会以 ResourceAlreadyExists 失败。
func (p *Provisioner) ensureThing(ctx context.Context, thingName string) error {
	if _, err := p.client.DescribeThing(ctx, &iot.DescribeThingInput{ThingName: aws.String(thingName)}); err == nil {
		return nil
	} else {
		var notFound *types.ResourceNotFoundException
		if !errors.As(err, &notFound) {
			return fmt.Errorf("describe thing: %w", err)
		}
	}
	if _, err := p.client.CreateThing(ctx, &iot.CreateThingInput{ThingName: aws.String(thingName)}); err != nil {
		var exists *types.ResourceAlreadyExistsException
		if errors.As(err, &exists) {
			return nil
		}
		return fmt.Errorf("create thing: %w", err)
	}
	return nil
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
