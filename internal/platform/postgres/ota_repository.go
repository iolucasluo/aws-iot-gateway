package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

type OTARepository struct{ pool *pgxpool.Pool }

func NewOTARepository(pool *pgxpool.Pool) (*OTARepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL OTA repository requires a pool")
	}
	return &OTARepository{pool: pool}, nil
}

func (repository *OTARepository) CreateFirmwareVersion(ctx context.Context, version ota.FirmwareVersion) (ota.FirmwareVersion, error) {
	var artifactKey, sourceURL any
	if version.ArtifactKey != "" {
		artifactKey = version.ArtifactKey
	}
	if version.SourceURL != "" {
		sourceURL = version.SourceURL
	}
	commandTag, err := repository.pool.Exec(ctx, `
		INSERT INTO ota_firmware_versions (
			id, created_by_internal_app_id, idempotency_key, pid, target_environment_id, target_environment_code, version, firmware_build,
			sha256, manifest_sha256, file_size, release_statement, signature,
			artifact_key, source_url, status, available_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)
		ON CONFLICT (created_by_internal_app_id, idempotency_key) DO NOTHING
	`, version.ID, version.CreatedByAppID, version.IdempotencyKey, version.PID, version.TargetEnvironmentID, version.TargetEnvironmentCode, version.Version,
		version.FirmwareBuild, version.SHA256, version.ManifestSHA256, version.FileSize,
		version.ReleaseStatement, version.Signature, artifactKey, sourceURL, version.Status,
		version.AvailableAt, version.CreatedAt)
	if err != nil {
		if uniqueViolation(err) {
			return ota.FirmwareVersion{}, ota.ErrConflict
		}
		return ota.FirmwareVersion{}, fmt.Errorf("create OTA firmware version: %w", err)
	}
	created, err := repository.GetFirmwareVersionForPublisher(ctx, version.CreatedByAppID, version.ID)
	if commandTag.RowsAffected() == 0 {
		created, err = repository.getFirmwareVersionByIdempotency(ctx, version.CreatedByAppID, version.IdempotencyKey)
	}
	if err != nil {
		return ota.FirmwareVersion{}, err
	}
	if created.PID != version.PID || created.TargetEnvironmentID != version.TargetEnvironmentID ||
		created.TargetEnvironmentCode != version.TargetEnvironmentCode || created.Version != version.Version ||
		created.FirmwareBuild != version.FirmwareBuild || created.SHA256 != version.SHA256 ||
		created.ManifestSHA256 != version.ManifestSHA256 || created.FileSize != version.FileSize ||
		created.ReleaseStatement != version.ReleaseStatement || created.Signature != version.Signature ||
		created.ArtifactKey != version.ArtifactKey || created.SourceURL != version.SourceURL {
		return ota.FirmwareVersion{}, ota.ErrConflict
	}
	return created, nil
}

func (repository *OTARepository) GetFirmwareVersionForPublisher(ctx context.Context, internalAppID, versionID string) (ota.FirmwareVersion, error) {
	return scanOTAFirmwareVersion(repository.pool.QueryRow(ctx, `
		SELECT id::text, created_by_internal_app_id, idempotency_key, pid,
		       COALESCE(target_environment_id,''), COALESCE(target_environment_code,''), version,
		       firmware_build, sha256, manifest_sha256, file_size, release_statement,
		       signature, COALESCE(artifact_key,''), COALESCE(source_url,''), status,
		       created_at, available_at
		FROM ota_firmware_versions
		WHERE id=$1 AND created_by_internal_app_id=$2
	`, versionID, internalAppID))
}

func (repository *OTARepository) getFirmwareVersionByIdempotency(ctx context.Context, internalAppID, idempotencyKey string) (ota.FirmwareVersion, error) {
	return scanOTAFirmwareVersion(repository.pool.QueryRow(ctx, `
		SELECT id::text, created_by_internal_app_id, idempotency_key, pid,
		       COALESCE(target_environment_id,''), COALESCE(target_environment_code,''), version,
		       firmware_build, sha256, manifest_sha256, file_size, release_statement,
		       signature, COALESCE(artifact_key,''), COALESCE(source_url,''), status,
		       created_at, available_at
		FROM ota_firmware_versions
		WHERE created_by_internal_app_id=$1 AND idempotency_key=$2
	`, internalAppID, idempotencyKey))
}

func (repository *OTARepository) CompleteFirmwareVersion(ctx context.Context, internalAppID, versionID string) (ota.FirmwareVersion, error) {
	commandTag, err := repository.pool.Exec(ctx, `
		UPDATE ota_firmware_versions
		SET status='available', available_at=COALESCE(available_at,now())
		WHERE id=$1 AND created_by_internal_app_id=$2 AND status IN ('uploading','available')
	`, versionID, internalAppID)
	if err != nil {
		return ota.FirmwareVersion{}, fmt.Errorf("complete OTA firmware version: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return ota.FirmwareVersion{}, ota.ErrNotFound
	}
	return repository.GetFirmwareVersionForPublisher(ctx, internalAppID, versionID)
}

func (repository *OTARepository) ListAvailableFirmwareVersions(ctx context.Context, pid string) ([]ota.FirmwareVersion, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT id::text, created_by_internal_app_id, idempotency_key, pid,
		       COALESCE(target_environment_id,''), COALESCE(target_environment_code,''), version,
		       firmware_build, sha256, manifest_sha256, file_size, release_statement,
		       signature, COALESCE(artifact_key,''), COALESCE(source_url,''), status,
		       created_at, available_at
		FROM ota_firmware_versions
		WHERE pid=$1 AND status='available'
		ORDER BY firmware_build DESC, id DESC
	`, pid)
	if err != nil {
		return nil, fmt.Errorf("list OTA firmware versions: %w", err)
	}
	defer rows.Close()
	result := make([]ota.FirmwareVersion, 0)
	for rows.Next() {
		version, err := scanOTAFirmwareVersion(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate OTA firmware versions: %w", err)
	}
	return result, nil
}

func (repository *OTARepository) CreateDeployment(ctx context.Context, deployment ota.Deployment) (ota.Deployment, ota.FirmwareVersion, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ota.Deployment{}, ota.FirmwareVersion{}, fmt.Errorf("begin OTA deployment: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var deviceSN, devicePID string
	var presenceOnline bool
	var presenceUpdatedAt *time.Time
	if err := transaction.QueryRow(ctx, `
		SELECT device_sn, pid, presence_online, presence_updated_at
		FROM device_registry WHERE id=$1 FOR UPDATE
	`, deployment.DeviceID).Scan(&deviceSN, &devicePID, &presenceOnline, &presenceUpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ota.Deployment{}, ota.FirmwareVersion{}, ota.ErrNotFound
		}
		return ota.Deployment{}, ota.FirmwareVersion{}, fmt.Errorf("locate OTA device: %w", err)
	}
	if deviceSN != deployment.DeviceSN {
		return ota.Deployment{}, ota.FirmwareVersion{}, ota.ErrNotFound
	}
	if !presenceOnline || presenceUpdatedAt == nil || presenceUpdatedAt.Before(time.Now().UTC().Add(-3*time.Minute)) {
		return ota.Deployment{}, ota.FirmwareVersion{}, ota.ErrDeviceOffline
	}
	version, err := scanOTAFirmwareVersion(transaction.QueryRow(ctx, `
		SELECT id::text, created_by_internal_app_id, idempotency_key, pid,
		       COALESCE(target_environment_id,''), COALESCE(target_environment_code,''), version,
		       firmware_build, sha256, manifest_sha256, file_size, release_statement,
		       signature, COALESCE(artifact_key,''), COALESCE(source_url,''), status,
		       created_at, available_at
		FROM ota_firmware_versions WHERE id=$1 AND status='available'
	`, deployment.FirmwareVersionID))
	if err != nil {
		return ota.Deployment{}, ota.FirmwareVersion{}, err
	}
	if version.PID != devicePID {
		return ota.Deployment{}, ota.FirmwareVersion{}, ota.ErrConflict
	}
	existing, err := scanOTADeployment(transaction.QueryRow(ctx, `
		SELECT od.id::text, od.internal_app_id, od.device_registry_id::text, d.device_sn,
		       od.firmware_version_id::text, od.target_version, od.target_build,
		       od.idempotency_key, od.state, COALESCE(od.failure_reason,''),
		       od.created_at, od.updated_at, od.completed_at
		FROM device_ota_deployments od
		JOIN device_registry d ON d.id=od.device_registry_id
		WHERE od.internal_app_id=$1 AND od.device_registry_id=$2 AND od.idempotency_key=$3
	`, deployment.InternalAppID, deployment.DeviceID, deployment.IdempotencyKey))
	if err == nil {
		if existing.FirmwareVersionID != deployment.FirmwareVersionID {
			return ota.Deployment{}, ota.FirmwareVersion{}, ota.ErrConflict
		}
		if err := transaction.Commit(ctx); err != nil {
			return ota.Deployment{}, ota.FirmwareVersion{}, fmt.Errorf("commit idempotent OTA deployment: %w", err)
		}
		return existing, version, nil
	}
	if !errors.Is(err, ota.ErrNotFound) {
		return ota.Deployment{}, ota.FirmwareVersion{}, err
	}
	deployment.TargetVersion = version.Version
	deployment.TargetBuild = version.FirmwareBuild
	if _, err := transaction.Exec(ctx, `
		INSERT INTO device_ota_deployments (
			id, device_registry_id, internal_app_id, firmware_version_id, idempotency_key,
			target_version, target_build, state, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
	`, deployment.ID, deployment.DeviceID, deployment.InternalAppID,
		deployment.FirmwareVersionID, deployment.IdempotencyKey, deployment.TargetVersion,
		deployment.TargetBuild, deployment.State, deployment.CreatedAt); err != nil {
		if uniqueViolation(err) {
			return ota.Deployment{}, ota.FirmwareVersion{}, ota.ErrConflict
		}
		return ota.Deployment{}, ota.FirmwareVersion{}, fmt.Errorf("create OTA deployment: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return ota.Deployment{}, ota.FirmwareVersion{}, fmt.Errorf("commit OTA deployment: %w", err)
	}
	return deployment, version, nil
}

func (repository *OTARepository) UpdateDeployment(ctx context.Context, deploymentID, state, reason string) error {
	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}
	commandTag, err := repository.pool.Exec(ctx, `
		UPDATE device_ota_deployments
		SET state=CASE WHEN $2='accepted' AND state <> 'pending' THEN state ELSE $2 END,
		    failure_reason=CASE WHEN $2='accepted' AND state <> 'pending' THEN failure_reason ELSE $3 END,
		    completed_at=CASE WHEN $2 IN ('failed','succeeded','rolled_back') THEN now() ELSE NULL END
		WHERE id=$1 AND (
		    ($2='accepted' AND state IN ('pending','accepted','downloading','verifying','installing')) OR
		    ($2='failed' AND state NOT IN ('succeeded','failed','rolled_back'))
		)
	`, deploymentID, state, reasonValue)
	if err != nil {
		return fmt.Errorf("update OTA deployment: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return ota.ErrConflict
	}
	return nil
}

func (repository *OTARepository) GetDeployment(ctx context.Context, internalAppID, deviceID, deploymentID string) (ota.Deployment, error) {
	return scanOTADeployment(repository.pool.QueryRow(ctx, `
		SELECT od.id::text, od.internal_app_id, od.device_registry_id::text, d.device_sn,
		       od.firmware_version_id::text, od.target_version, od.target_build,
		       od.idempotency_key, od.state, COALESCE(od.failure_reason,''),
		       od.created_at, od.updated_at, od.completed_at
		FROM device_ota_deployments od
		JOIN device_registry d ON d.id=od.device_registry_id
		WHERE od.id=$1 AND od.internal_app_id=$2 AND od.device_registry_id=$3
	`, deploymentID, internalAppID, deviceID))
}

type rowScanner interface{ Scan(...any) error }

func scanOTAFirmwareVersion(row rowScanner) (ota.FirmwareVersion, error) {
	var version ota.FirmwareVersion
	if err := row.Scan(
		&version.ID, &version.CreatedByAppID, &version.IdempotencyKey, &version.PID,
		&version.TargetEnvironmentID, &version.TargetEnvironmentCode, &version.Version, &version.FirmwareBuild, &version.SHA256, &version.ManifestSHA256,
		&version.FileSize, &version.ReleaseStatement, &version.Signature, &version.ArtifactKey,
		&version.SourceURL, &version.Status, &version.CreatedAt, &version.AvailableAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ota.FirmwareVersion{}, ota.ErrNotFound
		}
		return ota.FirmwareVersion{}, fmt.Errorf("scan OTA firmware version: %w", err)
	}
	return version, nil
}

func scanOTADeployment(row rowScanner) (ota.Deployment, error) {
	var deployment ota.Deployment
	if err := row.Scan(
		&deployment.ID, &deployment.InternalAppID, &deployment.DeviceID, &deployment.DeviceSN,
		&deployment.FirmwareVersionID, &deployment.TargetVersion, &deployment.TargetBuild,
		&deployment.IdempotencyKey, &deployment.State, &deployment.FailureReason,
		&deployment.CreatedAt, &deployment.UpdatedAt, &deployment.CompletedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ota.Deployment{}, ota.ErrNotFound
		}
		return ota.Deployment{}, fmt.Errorf("scan OTA deployment: %w", err)
	}
	return deployment, nil
}

func uniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}
