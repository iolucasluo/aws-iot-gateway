package project

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

// MemoryRepository 是不依赖 PostgreSQL 的实现，供单元测试与本地开发使用。
// 它复制真实实现必须保证的唯一性语义，不复制 SQL 细节。
type MemoryRepository struct {
	mutex          sync.Mutex
	projects       map[string]Project
	projectItems   map[string][]string
	products       map[string]string // productID -> pid（仅登记既有目录产品）
	productCatalog map[string]Product
	credentials    map[string]Credential
	manufacturers  map[string]struct{}
	dpProfiles     map[string]DPProfile
	dpDefinitions  map[string]DPDefinition        // id -> definition
	dpMembers      map[string]map[string]struct{} // profileCode -> set of definitionIDs
	portalAccounts map[string]portalaccount.Account
	webhooks       map[string]Webhook
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		projects:       map[string]Project{},
		projectItems:   map[string][]string{},
		products:       map[string]string{},
		productCatalog: map[string]Product{},
		credentials:    map[string]Credential{},
		manufacturers:  map[string]struct{}{},
		dpProfiles:     map[string]DPProfile{},
		dpDefinitions:  map[string]DPDefinition{},
		dpMembers:      map[string]map[string]struct{}{},
		portalAccounts: map[string]portalaccount.Account{},
		webhooks:       map[string]Webhook{},
	}
}

// AddProduct 与 AddManufacturer 供测试与本地开发登记既有事实源。
func (repository *MemoryRepository) AddProduct(productID, pid string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.products[productID] = pid
}

func (repository *MemoryRepository) AddManufacturer(manufacturerID string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.manufacturers[manufacturerID] = struct{}{}
}

func (repository *MemoryRepository) CreateProject(_ context.Context, project Project) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.projects[project.ID]; found {
		return ErrInvalidInput
	}
	repository.projects[project.ID] = project
	return nil
}

func (repository *MemoryRepository) FindProjectByID(_ context.Context, projectID string) (Project, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	project, found := repository.projects[projectID]
	if !found {
		return Project{}, ErrProjectNotFound
	}
	return project, nil
}

func (repository *MemoryRepository) ListProjects(_ context.Context, filter ProjectFilter) ([]Project, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	projects := make([]Project, 0, len(repository.projects))
	for _, project := range repository.projects {
		if filter.OwnerAccountID != "" && project.OwnerAccountID != filter.OwnerAccountID {
			continue
		}
		if filter.ManufacturerID != "" && project.ManufacturerID != filter.ManufacturerID {
			continue
		}
		if filter.Type != "" && project.Type != filter.Type {
			continue
		}
		if filter.Status != "" && project.Status != filter.Status {
			continue
		}
		projects = append(projects, project)
	}
	sort.Slice(projects, func(left, right int) bool {
		if projects[left].CreatedAt.Equal(projects[right].CreatedAt) {
			return projects[left].ID < projects[right].ID
		}
		return projects[left].CreatedAt.Before(projects[right].CreatedAt)
	})
	if filter.Limit > 0 && len(projects) > filter.Limit {
		projects = projects[:filter.Limit]
	}
	return projects, nil
}

func (repository *MemoryRepository) UpdateProject(_ context.Context, project Project) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.projects[project.ID]; !found {
		return ErrProjectNotFound
	}
	repository.projects[project.ID] = project
	return nil
}

func (repository *MemoryRepository) DeleteProject(_ context.Context, projectID string) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.projects[projectID]; !found {
		return ErrProjectNotFound
	}
	delete(repository.projects, projectID)
	delete(repository.projectItems, projectID)
	for id, cred := range repository.credentials {
		if cred.ProjectID == projectID {
			delete(repository.credentials, id)
		}
	}
	for id, wh := range repository.webhooks {
		if wh.ProjectID == projectID {
			delete(repository.webhooks, id)
		}
	}
	return nil
}

func (repository *MemoryRepository) AddProjectProduct(_ context.Context, _, projectID, productID string, _ time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.projects[projectID]; !found {
		return ErrProjectNotFound
	}
	if !repository.productExistsLocked(productID) {
		return ErrProductNotFound
	}
	for _, existing := range repository.projectItems[projectID] {
		if existing == productID {
			return ErrDuplicateProduct
		}
	}
	repository.projectItems[projectID] = append(repository.projectItems[projectID], productID)
	return nil
}

func (repository *MemoryRepository) RemoveProjectProduct(_ context.Context, projectID, productID string) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	items := repository.projectItems[projectID]
	for index, existing := range items {
		if existing == productID {
			repository.projectItems[projectID] = append(append([]string{}, items[:index]...), items[index+1:]...)
			return nil
		}
	}
	return ErrProductNotInProject
}

func (repository *MemoryRepository) ListProjectProductIDs(_ context.Context, projectID string) ([]string, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	return append([]string{}, repository.projectItems[projectID]...), nil
}

func (repository *MemoryRepository) ListProjectPIDs(_ context.Context, projectID string) ([]string, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	pids := make([]string, 0, len(repository.projectItems[projectID]))
	for _, productID := range repository.projectItems[projectID] {
		pids = append(pids, repository.pidForLocked(productID))
	}
	sort.Strings(pids)
	return pids, nil
}

// productExistsLocked 与 pidForLocked 同时覆盖两种登记方式：
// AddProduct 登记的既有目录产品，以及 CreateProduct 新建的产品。
// 生产实现只有 products 一张表，内存实现必须保持同样的可见性。
func (repository *MemoryRepository) productExistsLocked(productID string) bool {
	if _, found := repository.products[productID]; found {
		return true
	}
	_, found := repository.productCatalog[productID]
	return found
}

func (repository *MemoryRepository) pidForLocked(productID string) string {
	if pid, found := repository.products[productID]; found {
		return pid
	}
	return repository.productCatalog[productID].PID
}

func (repository *MemoryRepository) ManufacturerExists(_ context.Context, manufacturerID string) (bool, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	_, found := repository.manufacturers[manufacturerID]
	return found, nil
}

func (repository *MemoryRepository) CreateCredential(_ context.Context, credential Credential) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, existing := range repository.credentials {
		if existing.APIKey == credential.APIKey {
			return ErrInvalidInput
		}
	}
	repository.credentials[credential.ID] = credential
	return nil
}

func (repository *MemoryRepository) FindCredentialByID(_ context.Context, credentialID string) (Credential, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	credential, found := repository.credentials[credentialID]
	if !found {
		return Credential{}, ErrCredentialNotFound
	}
	return credential, nil
}

func (repository *MemoryRepository) FindCredentialByAPIKey(_ context.Context, apiKey string) (Credential, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, credential := range repository.credentials {
		if credential.APIKey == apiKey {
			return credential, nil
		}
	}
	return Credential{}, ErrCredentialNotFound
}

func (repository *MemoryRepository) ListCredentials(_ context.Context, projectID string) ([]Credential, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	credentials := make([]Credential, 0, len(repository.credentials))
	for _, credential := range repository.credentials {
		if credential.ProjectID != projectID {
			continue
		}
		credentials = append(credentials, credential)
	}
	sort.Slice(credentials, func(left, right int) bool {
		return credentials[left].CreatedAt.Before(credentials[right].CreatedAt)
	})
	return credentials, nil
}

func (repository *MemoryRepository) UpdateCredential(_ context.Context, credential Credential) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.credentials[credential.ID]; !found {
		return ErrCredentialNotFound
	}
	repository.credentials[credential.ID] = credential
	return nil
}

func (repository *MemoryRepository) TouchCredentialUsed(_ context.Context, credentialID string, at time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	credential, found := repository.credentials[credentialID]
	if !found {
		return ErrCredentialNotFound
	}
	moment := at
	credential.LastUsedAt = &moment
	repository.credentials[credentialID] = credential
	return nil
}

// AddDPProfile 与 AddPortalAccount 供测试与本地开发登记既有事实源。
func (repository *MemoryRepository) AddDPProfile(code, name string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.dpProfiles[code] = DPProfile{Code: code, Name: name}
}

func (repository *MemoryRepository) AddPortalAccount(account portalaccount.Account) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.portalAccounts[account.ID] = account
}

func (repository *MemoryRepository) FindPortalAccount(_ context.Context, accountID string) (portalaccount.Account, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	account, found := repository.portalAccounts[accountID]
	if !found {
		return portalaccount.Account{}, ErrOwnerNotFound
	}
	return account, nil
}

func (repository *MemoryRepository) ActiveDPProfileExists(_ context.Context, code string) (bool, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	_, found := repository.dpProfiles[code]
	return found, nil
}

func (repository *MemoryRepository) ListActiveDPProfiles(_ context.Context) ([]DPProfile, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	profiles := make([]DPProfile, 0, len(repository.dpProfiles))
	for _, profile := range repository.dpProfiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(left, right int) bool { return profiles[left].Code < profiles[right].Code })
	return profiles, nil
}

func (repository *MemoryRepository) ListDPDefinitionsForProfile(_ context.Context, profileCode, ownerAccountID string) ([]DPDefinition, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	memberIDs := repository.dpMembers[profileCode]
	defs := make([]DPDefinition, 0, len(memberIDs))
	for id := range memberIDs {
		def, ok := repository.dpDefinitions[id]
		if !ok {
			continue
		}
		if def.OwnerAccountID == "" || def.OwnerAccountID == ownerAccountID {
			defs = append(defs, def)
		}
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Code < defs[j].Code })
	return defs, nil
}

func (repository *MemoryRepository) CreateDPDefinition(_ context.Context, def DPDefinition, profileCodes []string) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.dpDefinitions[def.ID] = def
	for _, code := range profileCodes {
		if repository.dpMembers[code] == nil {
			repository.dpMembers[code] = map[string]struct{}{}
		}
		repository.dpMembers[code][def.ID] = struct{}{}
	}
	return nil
}

func (repository *MemoryRepository) DeleteDPDefinition(_ context.Context, id, ownerAccountID string) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	def, ok := repository.dpDefinitions[id]
	if !ok || def.OwnerAccountID != ownerAccountID {
		return ErrDPDefinitionNotFound
	}
	delete(repository.dpDefinitions, id)
	for code, members := range repository.dpMembers {
		delete(members, id)
		if len(members) == 0 {
			delete(repository.dpMembers, code)
		}
	}
	return nil
}

func (repository *MemoryRepository) CreateProduct(_ context.Context, product Product) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, existing := range repository.productCatalog {
		if existing.PID == product.PID {
			return fmt.Errorf("%w: pid already exists", ErrInvalidInput)
		}
	}
	repository.productCatalog[product.ID] = product
	return nil
}

func (repository *MemoryRepository) FindProductByPID(_ context.Context, pid string) (Product, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, product := range repository.productCatalog {
		if product.PID == pid {
			return product, nil
		}
	}
	return Product{}, ErrProductNotFound
}

func (repository *MemoryRepository) ListProducts(_ context.Context, filter ProductFilter) ([]Product, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	products := make([]Product, 0, len(repository.productCatalog))
	for _, product := range repository.productCatalog {
		if filter.OwnerAccountID != "" &&
			product.OwnerAccountID != filter.OwnerAccountID &&
			!(filter.IncludeGlobal && product.OwnerAccountID == "") {
			continue
		}
		if filter.Status != "" && product.Status != filter.Status {
			continue
		}
		if filter.PID != "" && product.PID != filter.PID {
			continue
		}
		products = append(products, product)
	}
	sort.Slice(products, func(left, right int) bool { return products[left].PID < products[right].PID })
	if filter.Limit > 0 && len(products) > filter.Limit {
		products = products[:filter.Limit]
	}
	return products, nil
}

func (repository *MemoryRepository) UpdateProduct(_ context.Context, product Product) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.productCatalog[product.ID]; !found {
		return ErrProductNotFound
	}
	repository.productCatalog[product.ID] = product
	return nil
}

func (repository *MemoryRepository) CreateWebhook(_ context.Context, webhook Webhook) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.projects[webhook.ProjectID]; !found {
		return ErrProjectNotFound
	}
	if _, found := repository.webhooks[webhook.ID]; found {
		return ErrInvalidInput
	}
	// 与生产一致：一个项目同时只能有一个 active 回调。
	if webhook.Status == StatusActive {
		for _, existing := range repository.webhooks {
			if existing.ProjectID == webhook.ProjectID && existing.Status == StatusActive {
				return fmt.Errorf("%w: an active webhook already exists for this project", ErrInvalidInput)
			}
		}
	}
	repository.webhooks[webhook.ID] = webhook
	return nil
}

func (repository *MemoryRepository) FindWebhookByID(_ context.Context, webhookID string) (Webhook, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	webhook, found := repository.webhooks[webhookID]
	if !found {
		return Webhook{}, ErrWebhookNotFound
	}
	return webhook, nil
}

func (repository *MemoryRepository) FindActiveWebhookByProject(_ context.Context, projectID string) (Webhook, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, webhook := range repository.webhooks {
		if webhook.ProjectID == projectID && webhook.Status == StatusActive {
			return webhook, nil
		}
	}
	return Webhook{}, ErrWebhookNotFound
}

func (repository *MemoryRepository) ListWebhooks(_ context.Context, projectID string) ([]Webhook, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	webhooks := make([]Webhook, 0, 2)
	for _, webhook := range repository.webhooks {
		if webhook.ProjectID != projectID {
			continue
		}
		webhooks = append(webhooks, webhook)
	}
	sort.Slice(webhooks, func(left, right int) bool { return webhooks[left].CreatedAt.Before(webhooks[right].CreatedAt) })
	return webhooks, nil
}

func (repository *MemoryRepository) UpdateWebhook(_ context.Context, webhook Webhook) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.webhooks[webhook.ID]; !found {
		return ErrWebhookNotFound
	}
	if webhook.Status == StatusActive {
		for id, existing := range repository.webhooks {
			if id != webhook.ID && existing.ProjectID == webhook.ProjectID && existing.Status == StatusActive {
				return fmt.Errorf("%w: an active webhook already exists for this project", ErrInvalidInput)
			}
		}
	}
	repository.webhooks[webhook.ID] = webhook
	return nil
}

func (repository *MemoryRepository) DeleteWebhook(_ context.Context, webhookID string) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.webhooks[webhookID]; !found {
		return ErrWebhookNotFound
	}
	delete(repository.webhooks, webhookID)
	return nil
}

func (repository *MemoryRepository) FindActiveCredentialForPermission(_ context.Context, projectID, permission string) (Credential, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, credential := range repository.credentials {
		if credential.ProjectID != projectID || credential.Status != StatusActive {
			continue
		}
		for _, granted := range credential.Permissions {
			if granted == permission {
				return credential, nil
			}
		}
	}
	return Credential{}, ErrCredentialNotFound
}
