package awsiot

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"github.com/aws/aws-sdk-go-v2/service/iot/types"
)

type CertificateUpdater interface {
	UpdateCertificate(context.Context, *iot.UpdateCertificateInput, ...func(*iot.Options)) (*iot.UpdateCertificateOutput, error)
}

type Lifecycle struct{ client CertificateUpdater }

func NewLifecycle(client CertificateUpdater) (*Lifecycle, error) {
	if client == nil {
		return nil, fmt.Errorf("AWS IoT lifecycle requires a client")
	}
	return &Lifecycle{client: client}, nil
}

func (lifecycle *Lifecycle) RevokeCertificate(ctx context.Context, certificateID string) error {
	if certificateID == "" {
		return fmt.Errorf("certificate id is empty")
	}
	if _, err := lifecycle.client.UpdateCertificate(ctx, &iot.UpdateCertificateInput{
		CertificateId: aws.String(certificateID), NewStatus: types.CertificateStatusRevoked,
	}); err != nil {
		return fmt.Errorf("revoke AWS IoT certificate: %w", err)
	}
	return nil
}
