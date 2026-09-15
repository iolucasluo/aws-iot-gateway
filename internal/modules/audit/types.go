package audit

import (
	"errors"
	"time"
)

// 动作主体类型，与 005 迁移的 audit_logs_actor_kind_check 一致。
const (
	ActorPortalAccount = "portal_account"
	ActorInternalApp   = "internal_app"
	ActorSystem        = "system"
)

// 结果，与 001 迁移的 audit_logs_outcome_check 一致。
const (
	OutcomeAccepted  = "accepted"
	OutcomeSucceeded = "succeeded"
	OutcomeRejected  = "rejected"
	OutcomeFailed    = "failed"
)

var ErrInvalidEntry = errors.New("invalid audit entry")

// Entry 是一条审计事实。它是 append-only 的：没有更新语义。
//
// Metadata 只允许放可公开的结构化上下文（路径、状态码、字段名等）；
// secret、token、口令一律不得进入审计。
type Entry struct {
	ID             string
	ActorKind      string
	ActorAccountID string
	ActorLabel     string
	InternalAppID  string
	Operation      string
	ResourceType   string
	ResourceID     string
	RequestID      string
	Outcome        string
	ErrorCode      string
	Metadata       map[string]any
	CreatedAt      time.Time
}
