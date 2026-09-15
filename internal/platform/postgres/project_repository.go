package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

// ProjectRepository 是平台面项目域的 PostgreSQL 实现。
type ProjectRepository struct{ pool *pgxpool.Pool }

func NewProjectRepository(pool *pgxpool.Pool) (*ProjectRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL project repository requires a pool")
	}
	return &ProjectRepository{pool: pool}, nil
}

const projectColumns = `
	id::text, owner_account_id::text, project_type, name, COALESCE(description, ''),
	COALESCE(manufacturer_id::text, ''), status, created_at, updated_at
`

func (repository *ProjectRepository) CreateProject(ctx context.Context, value project.Project) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO projects (id, owner_account_id, project_type, name, description, manufacturer_id, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,'')::uuid,$7,$8,$9)
	`, value.ID, value.OwnerAccountID, string(value.Type), value.Name, value.Description,
		value.ManufacturerID, value.Status, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	return nil
}

func (repository *ProjectRepository) FindProjectByID(ctx context.Context, projectID string) (project.Project, error) {
	return scanProject(repository.pool.QueryRow(ctx, `
		SELECT `+projectColumns+` FROM projects WHERE id=$1
	`, projectID))
}

func (repository *ProjectRepository) ListProjects(ctx context.Context, filter project.ProjectFilter) ([]project.Project, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT `+projectColumns+`
		FROM projects
		WHERE ($1='' OR owner_account_id=$1::uuid)
		  AND ($2='' OR manufacturer_id=$2::uuid)
		  AND ($3='' OR project_type=$3)
		  AND ($4='' OR status=$4)
		ORDER BY created_at ASC, id ASC
		LIMIT $5
	`, filter.OwnerAccountID, filter.ManufacturerID, string(filter.Type), filter.Status, limit)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	projects := make([]project.Project, 0, limit)
	for rows.Next() {
		value, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

func (repository *ProjectRepository) UpdateProject(ctx context.Context, value project.Project) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE projects SET name=$2, description=NULLIF($3,''), status=$4, updated_at=$5 WHERE id=$1
	`, value.ID, value.Name, value.Description, value.Status, value.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return project.ErrProjectNotFound
	}
	return nil
}

func (repository *ProjectRepository) AddProjectProduct(ctx context.Context, id, projectID, productID string, at time.Time) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO project_products (id, project_id, product_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
	`, id, projectID, productID, at)
	if err != nil {
		switch {
		case isUniqueViolation(err):
			return project.ErrDuplicateProduct
		case isForeignKeyViolation(err):
			// 既可能是 project 不存在，也可能是 product 不存在；分别确认后再返回语义错误。
			if exists, checkErr := repository.projectExists(ctx, projectID); checkErr == nil && !exists {
				return project.ErrProjectNotFound
			}
			return project.ErrProductNotFound
		default:
			return fmt.Errorf("attach project product: %w", err)
		}
	}
	return nil
}

func (repository *ProjectRepository) RemoveProjectProduct(ctx context.Context, projectID, productID string) error {
	tag, err := repository.pool.Exec(ctx, `
		DELETE FROM project_products WHERE project_id=$1 AND product_id=$2
	`, projectID, productID)
	if err != nil {
		return fmt.Errorf("detach project product: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return project.ErrProductNotInProject
	}
	return nil
}

func (repository *ProjectRepository) ListProjectProductIDs(ctx context.Context, projectID string) ([]string, error) {
	return repository.queryStrings(ctx, `
		SELECT product_id::text FROM project_products WHERE project_id=$1 ORDER BY created_at ASC, product_id ASC
	`, projectID)
}

func (repository *ProjectRepository) ListProjectPIDs(ctx context.Context, projectID string) ([]string, error) {
	return repository.queryStrings(ctx, `
		SELECT p.pid FROM project_products pp JOIN products p ON p.id = pp.product_id
		WHERE pp.project_id=$1 ORDER BY p.pid ASC
	`, projectID)
}

func (repository *ProjectRepository) ManufacturerExists(ctx context.Context, manufacturerID string) (bool, error) {
	var exists bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM manufacturers WHERE id=$1)
	`, manufacturerID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check manufacturer existence: %w", err)
	}
	return exists, nil
}

const credentialColumns = `
	id::text, project_id::text, name, api_key, api_secret_ciphertext, kek_id,
	permissions, status, expires_at, last_used_at, created_at, updated_at
`

func (repository *ProjectRepository) CreateCredential(ctx context.Context, value project.Credential) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO api_credentials (
			id, project_id, name, api_key, api_secret_ciphertext, kek_id, permissions,
			status, expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,COALESCE($7::text[], '{}'::text[]),$8,$9,$10,$11)
	`, value.ID, value.ProjectID, value.Name, value.APIKey, value.SecretCiphertext, value.KEKID,
		value.Permissions, value.Status, value.ExpiresAt, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: api key already exists", project.ErrInvalidInput)
		}
		return fmt.Errorf("create credential: %w", err)
	}
	return nil
}

func (repository *ProjectRepository) FindCredentialByID(ctx context.Context, credentialID string) (project.Credential, error) {
	return scanCredential(repository.pool.QueryRow(ctx, `
		SELECT `+credentialColumns+` FROM api_credentials WHERE id=$1
	`, credentialID))
}

// FindCredentialByAPIKey 不过滤 status：调用方需要区分"不存在"与"存在但已停用"。
func (repository *ProjectRepository) FindCredentialByAPIKey(ctx context.Context, apiKey string) (project.Credential, error) {
	return scanCredential(repository.pool.QueryRow(ctx, `
		SELECT `+credentialColumns+` FROM api_credentials WHERE api_key=$1
	`, apiKey))
}

func (repository *ProjectRepository) ListCredentials(ctx context.Context, projectID string) ([]project.Credential, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT `+credentialColumns+` FROM api_credentials WHERE project_id=$1
		ORDER BY created_at ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()
	credentials := make([]project.Credential, 0, 4)
	for rows.Next() {
		value, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	return credentials, nil
}

func (repository *ProjectRepository) UpdateCredential(ctx context.Context, value project.Credential) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE api_credentials SET name=$2, status=$3, permissions=$4, expires_at=$5, updated_at=$6 WHERE id=$1
	`, value.ID, value.Name, value.Status, value.Permissions, value.ExpiresAt, value.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return project.ErrCredentialNotFound
	}
	return nil
}

func (repository *ProjectRepository) TouchCredentialUsed(ctx context.Context, credentialID string, at time.Time) error {
	if _, err := repository.pool.Exec(ctx, `
		UPDATE api_credentials SET last_used_at=$2, updated_at=$2 WHERE id=$1
	`, credentialID, at); err != nil {
		return fmt.Errorf("touch credential: %w", err)
	}
	return nil
}

func (repository *ProjectRepository) projectExists(ctx context.Context, projectID string) (bool, error) {
	var exists bool
	if err := repository.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM projects WHERE id=$1)`, projectID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check project existence: %w", err)
	}
	return exists, nil
}

func (repository *ProjectRepository) queryStrings(ctx context.Context, sql string, argument string) ([]string, error) {
	rows, err := repository.pool.Query(ctx, sql, argument)
	if err != nil {
		return nil, fmt.Errorf("query project strings: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0, 4)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan project string: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query project strings: %w", err)
	}
	return values, nil
}

func scanProject(row pgx.Row) (project.Project, error) {
	var value project.Project
	var projectType string
	err := row.Scan(&value.ID, &value.OwnerAccountID, &projectType, &value.Name, &value.Description,
		&value.ManufacturerID, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Project{}, project.ErrProjectNotFound
	}
	if err != nil {
		return project.Project{}, fmt.Errorf("scan project: %w", err)
	}
	value.Type = project.ProjectType(projectType)
	return value, nil
}

func scanCredential(row pgx.Row) (project.Credential, error) {
	var value project.Credential
	err := row.Scan(&value.ID, &value.ProjectID, &value.Name, &value.APIKey, &value.SecretCiphertext,
		&value.KEKID, &value.Permissions, &value.Status, &value.ExpiresAt, &value.LastUsedAt,
		&value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Credential{}, project.ErrCredentialNotFound
	}
	if err != nil {
		return project.Credential{}, fmt.Errorf("scan credential: %w", err)
	}
	return value, nil
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23503"
}
