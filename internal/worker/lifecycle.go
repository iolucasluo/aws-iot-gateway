package worker

import (
	"context"
	"errors"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
)

type CertificateRevoker interface {
	RevokeCertificate(context.Context, string) error
}

type LifecycleWorker struct {
	repository *postgres.LifecycleRepository
	revoker    CertificateRevoker
}

func NewLifecycleWorker(repository *postgres.LifecycleRepository, revoker CertificateRevoker) *LifecycleWorker {
	return &LifecycleWorker{repository: repository, revoker: revoker}
}

func (worker *LifecycleWorker) PollOnce(ctx context.Context) error {
	if worker == nil || worker.repository == nil || worker.revoker == nil {
		return nil
	}
	job, err := worker.repository.Claim(ctx)
	if errors.Is(err, postgres.ErrLifecycleJobNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := worker.revoker.RevokeCertificate(ctx, job.CertificateID); err != nil {
		_ = worker.repository.Retry(ctx, job.ID, err.Error(), job.Attempts)
		return err
	}
	return worker.repository.Complete(ctx, job.ID)
}
