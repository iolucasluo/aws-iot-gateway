package portalaccount

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
	accounts      map[string]Account
	sessions      map[string]Session
	setupToken    map[string]SetupToken
	manufacturers map[string]struct{}
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		accounts:      map[string]Account{},
		sessions:      map[string]Session{},
		setupToken:    map[string]SetupToken{},
		manufacturers: map[string]struct{}{},
	}
}

// AddManufacturer 供测试与本地开发登记厂商；厂商主数据在 P4 由制造面拥有。
func (repository *MemoryRepository) AddManufacturer(manufacturerID string) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.manufacturers[manufacturerID] = struct{}{}
}

func (repository *MemoryRepository) ManufacturerExists(_ context.Context, manufacturerID string) (bool, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	_, found := repository.manufacturers[manufacturerID]
	return found, nil
}

func (repository *MemoryRepository) CreateAccount(_ context.Context, account Account) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, existing := range repository.accounts {
		if existing.Email == account.Email {
			return ErrEmailTaken
		}
	}
	repository.accounts[account.ID] = account
	return nil
}

func (repository *MemoryRepository) FindAccountByEmail(_ context.Context, email string) (Account, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, account := range repository.accounts {
		if account.Email == email {
			return account, nil
		}
	}
	return Account{}, ErrAccountNotFound
}

func (repository *MemoryRepository) FindAccountByID(_ context.Context, accountID string) (Account, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	account, found := repository.accounts[accountID]
	if !found {
		return Account{}, ErrAccountNotFound
	}
	return account, nil
}

func (repository *MemoryRepository) ListAccounts(_ context.Context, filter AccountFilter) ([]Account, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	accounts := make([]Account, 0, len(repository.accounts))
	for _, account := range repository.accounts {
		if filter.Kind != "" && account.Kind != filter.Kind {
			continue
		}
		if filter.Status != "" && account.Status != filter.Status {
			continue
		}
		if filter.ManufacturerID != "" && account.ManufacturerID != filter.ManufacturerID {
			continue
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(left, right int) bool {
		if accounts[left].CreatedAt.Equal(accounts[right].CreatedAt) {
			return accounts[left].ID < accounts[right].ID
		}
		return accounts[left].CreatedAt.Before(accounts[right].CreatedAt)
	})
	if filter.Limit > 0 && len(accounts) > filter.Limit {
		accounts = accounts[:filter.Limit]
	}
	return accounts, nil
}

func (repository *MemoryRepository) UpdateAccount(_ context.Context, account Account) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.accounts[account.ID]; !found {
		return ErrAccountNotFound
	}
	for id, existing := range repository.accounts {
		if id != account.ID && existing.Email == account.Email {
			return ErrEmailTaken
		}
	}
	repository.accounts[account.ID] = account
	return nil
}

func (repository *MemoryRepository) MarkAccountLogin(_ context.Context, accountID string, at time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	account, found := repository.accounts[accountID]
	if !found {
		return ErrAccountNotFound
	}
	moment := at
	account.LastLoginAt = &moment
	account.UpdatedAt = at
	repository.accounts[accountID] = account
	return nil
}

func (repository *MemoryRepository) CreateSession(_ context.Context, session Session) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.sessions[session.ID] = session
	return nil
}

func (repository *MemoryRepository) FindSessionByTokenHash(_ context.Context, tokenHash [32]byte) (Session, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, session := range repository.sessions {
		if session.TokenHash == tokenHash {
			return session, nil
		}
	}
	return Session{}, ErrSessionNotFound
}

func (repository *MemoryRepository) TouchSession(_ context.Context, sessionID string, at time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	session, found := repository.sessions[sessionID]
	if !found {
		return ErrSessionNotFound
	}
	session.LastSeenAt = at
	repository.sessions[sessionID] = session
	return nil
}

func (repository *MemoryRepository) RevokeSession(_ context.Context, sessionID string, at time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	session, found := repository.sessions[sessionID]
	if !found {
		return nil
	}
	moment := at
	session.RevokedAt = &moment
	repository.sessions[sessionID] = session
	return nil
}

func (repository *MemoryRepository) RevokeAccountSessions(_ context.Context, accountID string, at time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	moment := at
	for id, session := range repository.sessions {
		if session.AccountID != accountID || session.RevokedAt != nil {
			continue
		}
		session.RevokedAt = &moment
		repository.sessions[id] = session
	}
	return nil
}

func (repository *MemoryRepository) CreateSetupToken(_ context.Context, token SetupToken) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.setupToken[token.ID] = token
	return nil
}

func (repository *MemoryRepository) FindSetupTokenByHash(_ context.Context, tokenHash [32]byte) (SetupToken, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, token := range repository.setupToken {
		if token.TokenHash == tokenHash {
			return token, nil
		}
	}
	return SetupToken{}, ErrSetupTokenNotFound
}

func (repository *MemoryRepository) ConsumeSetupToken(_ context.Context, tokenID string, at time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	token, found := repository.setupToken[tokenID]
	if !found {
		return ErrSetupTokenNotFound
	}
	if token.UsedAt != nil {
		return ErrSetupTokenUsed
	}
	moment := at
	token.UsedAt = &moment
	repository.setupToken[tokenID] = token
	return nil
}

// countSessions 供测试断言"口令变更后所有会话失效"这类不变量。
func (repository *MemoryRepository) countActiveSessions(accountID string) int {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	count := 0
	for _, session := range repository.sessions {
		if session.AccountID == accountID && session.RevokedAt == nil {
			count++
		}
	}
	return count
}
