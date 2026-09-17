package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
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

const productColumns = `
	id::text, pid, COALESCE(dp_profile_code, ''), name, status, capabilities,
	COALESCE(owner_account_id::text, ''), COALESCE(category, ''), created_at, updated_at
`

func (repository *ProjectRepository) FindPortalAccount(ctx context.Context, accountID string) (portalaccount.Account, error) {
	var account portalaccount.Account
	var kind string
	err := repository.pool.QueryRow(ctx, `
		SELECT id::text, kind, role, email, COALESCE(password_hash, ''), COALESCE(display_name, ''),
		       COALESCE(manufacturer_id::text, ''), status, last_login_at, created_at, updated_at
		FROM portal_accounts WHERE id=$1
	`, accountID).Scan(&account.ID, &kind, &account.Role, &account.Email, &account.PasswordHash,
		&account.DisplayName, &account.ManufacturerID, &account.Status, &account.LastLoginAt,
		&account.CreatedAt, &account.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalaccount.Account{}, project.ErrOwnerNotFound
	}
	if err != nil {
		return portalaccount.Account{}, fmt.Errorf("find portal account: %w", err)
	}
	account.Kind = portalaccount.AccountKind(kind)
	return account, nil
}

func (repository *ProjectRepository) ActiveDPProfileExists(ctx context.Context, code string) (bool, error) {
	var exists bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM dp_profiles WHERE code=$1 AND status='active')
	`, code).Scan(&exists); err != nil {
		return false, fmt.Errorf("check dp profile existence: %w", err)
	}
	return exists, nil
}

func (repository *ProjectRepository) ListActiveDPProfiles(ctx context.Context) ([]project.DPProfile, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT code, name FROM dp_profiles WHERE status='active' ORDER BY code ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list active dp profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]project.DPProfile, 0, 4)
	for rows.Next() {
		var profile project.DPProfile
		if err := rows.Scan(&profile.Code, &profile.Name); err != nil {
			return nil, fmt.Errorf("scan dp profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active dp profiles: %w", err)
	}
	return profiles, nil
}

func (repository *ProjectRepository) CreateProduct(ctx context.Context, product project.Product) error {
	capabilities, err := encodeCapabilities(product.Capabilities)
	if err != nil {
		return err
	}
	_, err = repository.pool.Exec(ctx, `
		INSERT INTO products (
			id, pid, dp_profile_code, name, status, capabilities, owner_account_id, category, created_at, updated_at
		) VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,NULLIF($7,'')::uuid,NULLIF($8,''),$9,$10)
	`, product.ID, product.PID, product.DPProfileCode, product.Name, product.Status, capabilities,
		product.OwnerAccountID, product.Category, product.CreatedAt, product.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: pid already exists", project.ErrInvalidInput)
		}
		return fmt.Errorf("create product: %w", err)
	}
	return nil
}

func (repository *ProjectRepository) FindProductByPID(ctx context.Context, pid string) (project.Product, error) {
	return scanProduct(repository.pool.QueryRow(ctx, `
		SELECT `+productColumns+` FROM products WHERE pid=$1
	`, pid))
}

func (repository *ProjectRepository) ListProducts(ctx context.Context, filter project.ProductFilter) ([]project.Product, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT `+productColumns+`
		FROM products
		WHERE ($1='' OR owner_account_id=$1::uuid)
		  AND ($2='' OR status=$2)
		  AND ($3='' OR pid=$3)
		ORDER BY pid ASC
		LIMIT $4
	`, filter.OwnerAccountID, filter.Status, filter.PID, limit)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	defer rows.Close()
	products := make([]project.Product, 0, limit)
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	return products, nil
}

func (repository *ProjectRepository) UpdateProduct(ctx context.Context, product project.Product) error {
	capabilities, err := encodeCapabilities(product.Capabilities)
	if err != nil {
		return err
	}
	tag, err := repository.pool.Exec(ctx, `
		UPDATE products
		SET name=$2, status=$3, capabilities=$4, owner_account_id=NULLIF($5,'')::uuid,
		    category=NULLIF($6,''), updated_at=$7
		WHERE id=$1
	`, product.ID, product.Name, product.Status, capabilities, product.OwnerAccountID,
		product.Category, product.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update product: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return project.ErrProductNotFound
	}
	return nil
}

// encodeCapabilities 保证列内容始终是 JSON 数组：null 会违反 products_capabilities_check。
func encodeCapabilities(capabilities []string) ([]byte, error) {
	if capabilities == nil {
		capabilities = []string{}
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return nil, fmt.Errorf("encode product capabilities: %w", err)
	}
	return encoded, nil
}

func scanProduct(row pgx.Row) (project.Product, error) {
	var product project.Product
	var capabilities []byte
	err := row.Scan(&product.ID, &product.PID, &product.DPProfileCode, &product.Name, &product.Status,
		&capabilities, &product.OwnerAccountID, &product.Category, &product.CreatedAt, &product.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Product{}, project.ErrProductNotFound
	}
	if err != nil {
		return project.Product{}, fmt.Errorf("scan product: %w", err)
	}
	if len(capabilities) > 0 {
		if err := json.Unmarshal(capabilities, &product.Capabilities); err != nil {
			return project.Product{}, fmt.Errorf("decode product capabilities: %w", err)
		}
	}
	if product.Capabilities == nil {
		product.Capabilities = []string{}
	}
	return product, nil
}

const webhookColumns = `
	id::text, project_id::text, callback_url, secret_ciphertext, kek_id, event_types, status, created_at, updated_at
`

func (repository *ProjectRepository) CreateWebhook(ctx context.Context, webhook project.Webhook) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO project_webhooks (
			id, project_id, callback_url, secret_ciphertext, kek_id, event_types, status, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,COALESCE($6::text[], '{}'::text[]),$7,$8,$9)
	`, webhook.ID, webhook.ProjectID, webhook.CallbackURL, webhook.SecretCiphertext, webhook.KEKID,
		webhook.EventTypes, webhook.Status, webhook.CreatedAt, webhook.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: an active webhook already exists for this project", project.ErrInvalidInput)
		}
		return fmt.Errorf("create project webhook: %w", err)
	}
	return nil
}

func (repository *ProjectRepository) FindWebhookByID(ctx context.Context, webhookID string) (project.Webhook, error) {
	return scanWebhook(repository.pool.QueryRow(ctx, `
		SELECT `+webhookColumns+` FROM project_webhooks WHERE id=$1
	`, webhookID))
}

func (repository *ProjectRepository) FindActiveWebhookByProject(ctx context.Context, projectID string) (project.Webhook, error) {
	return scanWebhook(repository.pool.QueryRow(ctx, `
		SELECT `+webhookColumns+` FROM project_webhooks
		WHERE project_id=$1 AND status='active'
	`, projectID))
}

func (repository *ProjectRepository) ListWebhooks(ctx context.Context, projectID string) ([]project.Webhook, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT `+webhookColumns+` FROM project_webhooks WHERE project_id=$1
		ORDER BY created_at ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project webhooks: %w", err)
	}
	defer rows.Close()
	webhooks := make([]project.Webhook, 0, 2)
	for rows.Next() {
		webhook, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		webhooks = append(webhooks, webhook)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list project webhooks: %w", err)
	}
	return webhooks, nil
}

func (repository *ProjectRepository) UpdateWebhook(ctx context.Context, webhook project.Webhook) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE project_webhooks
		SET callback_url=$2, event_types=COALESCE($3::text[], '{}'::text[]), status=$4, updated_at=$5
		WHERE id=$1
	`, webhook.ID, webhook.CallbackURL, webhook.EventTypes, webhook.Status, webhook.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: an active webhook already exists for this project", project.ErrInvalidInput)
		}
		return fmt.Errorf("update project webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return project.ErrWebhookNotFound
	}
	return nil
}

func (repository *ProjectRepository) DeleteWebhook(ctx context.Context, webhookID string) error {
	tag, err := repository.pool.Exec(ctx, `DELETE FROM project_webhooks WHERE id=$1`, webhookID)
	if err != nil {
		return fmt.Errorf("delete project webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return project.ErrWebhookNotFound
	}
	return nil
}

func scanWebhook(row pgx.Row) (project.Webhook, error) {
	var webhook project.Webhook
	err := row.Scan(&webhook.ID, &webhook.ProjectID, &webhook.CallbackURL, &webhook.SecretCiphertext,
		&webhook.KEKID, &webhook.EventTypes, &webhook.Status, &webhook.CreatedAt, &webhook.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Webhook{}, project.ErrWebhookNotFound
	}
	if err != nil {
		return project.Webhook{}, fmt.Errorf("scan project webhook: %w", err)
	}
	return webhook, nil
}

// FindActiveCredentialForPermission 返回项目下第一条具备该权限的 active 凭证。
func (repository *ProjectRepository) FindActiveCredentialForPermission(ctx context.Context, projectID, permission string) (project.Credential, error) {
	return scanCredential(repository.pool.QueryRow(ctx, `
		SELECT `+credentialColumns+`
		FROM api_credentials
		WHERE project_id=$1 AND status='active' AND $2 = ANY(permissions)
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`, projectID, permission))
}
