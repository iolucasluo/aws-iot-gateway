package portalaccount

import (
	"context"
	"time"
)

// AccountFilter 是账号列表的显式过滤条件；空字段表示不过滤。
// ManufacturerID 由调用方按 portal scope 派生，不接受客户端直接传入的越权值。
type AccountFilter struct {
	Kind           AccountKind
	Status         string
	ManufacturerID string
	Limit          int
}

// Repository 是账号域的持久化边界。实现必须保证 email 与 token hash 的唯一性，
// 并把唯一性冲突转成 ErrEmailTaken，而不是让调用方解析数据库错误。
type Repository interface {
	CreateAccount(ctx context.Context, account Account) error
	FindAccountByEmail(ctx context.Context, email string) (Account, error)
	FindAccountByID(ctx context.Context, accountID string) (Account, error)
	ListAccounts(ctx context.Context, filter AccountFilter) ([]Account, error)
	UpdateAccount(ctx context.Context, account Account) error
	MarkAccountLogin(ctx context.Context, accountID string, at time.Time) error

	CreateSession(ctx context.Context, session Session) error
	FindSessionByTokenHash(ctx context.Context, tokenHash [32]byte) (Session, error)
	TouchSession(ctx context.Context, sessionID string, at time.Time) error
	RevokeSession(ctx context.Context, sessionID string, at time.Time) error
	RevokeAccountSessions(ctx context.Context, accountID string, at time.Time) error

	CreateSetupToken(ctx context.Context, token SetupToken) error
	FindSetupTokenByHash(ctx context.Context, tokenHash [32]byte) (SetupToken, error)
	ConsumeSetupToken(ctx context.Context, tokenID string, at time.Time) error
}
