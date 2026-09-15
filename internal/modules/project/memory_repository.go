package project

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryRepository 是不依赖 PostgreSQL 的实现，供单元测试与本地开发使用。
// 它复制真实实现必须保证的唯一性语义，不复制 SQL 细节。
type MemoryRepository struct {
	mutex         sync.Mutex
	projects      map[string]Project
	projectItems  map[string][]string
	products      map[string]string // productID -> pid
	credentials   map[string]Credential
	manufacturers map[string]struct{}
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		projects:      map[string]Project{},
		projectItems:  map[string][]string{},
		products:      map[string]string{},
		credentials:   map[string]Credential{},
		manufacturers: map[string]struct{}{},
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

func (repository *MemoryRepository) AddProjectProduct(_ context.Context, _, projectID, productID string, _ time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.projects[projectID]; !found {
		return ErrProjectNotFound
	}
	if _, found := repository.products[productID]; !found {
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
		pids = append(pids, repository.products[productID])
	}
	sort.Strings(pids)
	return pids, nil
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
