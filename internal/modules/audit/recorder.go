package audit

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	maxOperationLength = 96
	maxResourceType    = 64
	maxResourceID      = 128
	maxRequestID       = 128
	maxActorLabel      = 320
	maxErrorCode       = 64
)

// Repository 是审计事实的写入边界；实现只需要 INSERT。
type Repository interface {
	InsertAuditLog(ctx context.Context, entry Entry) error
}

// Recorder 校验并写入审计事实。
//
// 写入失败不会让业务请求失败（审计不可用不应该变成登录不可用），
// 但调用方必须把错误抛给日志/告警，不能静默吞掉。
type Recorder struct {
	repository Repository
	now        func() time.Time
	newID      func() (string, error)
}

func NewRecorder(repository Repository, now func() time.Time, newID func() (string, error)) (*Recorder, error) {
	if repository == nil {
		return nil, fmt.Errorf("audit repository is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		return nil, fmt.Errorf("audit id generator is required")
	}
	return &Recorder{repository: repository, now: now, newID: newID}, nil
}

// Record 归一化并落库。任何校验失败都返回 ErrInvalidEntry，不写半条记录。
func (recorder *Recorder) Record(ctx context.Context, entry Entry) error {
	entry.Operation = strings.TrimSpace(entry.Operation)
	entry.ActorKind = strings.TrimSpace(entry.ActorKind)
	entry.ActorAccountID = strings.TrimSpace(entry.ActorAccountID)
	entry.ActorLabel = strings.TrimSpace(entry.ActorLabel)
	entry.InternalAppID = strings.TrimSpace(entry.InternalAppID)
	entry.ResourceType = strings.TrimSpace(entry.ResourceType)
	entry.ResourceID = strings.TrimSpace(entry.ResourceID)
	entry.RequestID = strings.TrimSpace(entry.RequestID)
	entry.ErrorCode = strings.TrimSpace(entry.ErrorCode)
	if entry.Operation == "" || len(entry.Operation) > maxOperationLength {
		return fmt.Errorf("%w: operation is required and bounded", ErrInvalidEntry)
	}
	if !validActorKind(entry.ActorKind) {
		return fmt.Errorf("%w: unknown actor kind %q", ErrInvalidEntry, entry.ActorKind)
	}
	if !validOutcome(entry.Outcome) {
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalidEntry, entry.Outcome)
	}
	for name, field := range map[string]struct {
		value string
		limit int
	}{
		"resource type": {entry.ResourceType, maxResourceType},
		"resource id":   {entry.ResourceID, maxResourceID},
		"request id":    {entry.RequestID, maxRequestID},
		"actor label":   {entry.ActorLabel, maxActorLabel},
		"error code":    {entry.ErrorCode, maxErrorCode},
	} {
		if len(field.value) > field.limit {
			return fmt.Errorf("%w: %s exceeds %d characters", ErrInvalidEntry, name, field.limit)
		}
	}
	if entry.ActorKind == ActorPortalAccount && entry.ActorAccountID == "" && entry.ActorLabel == "" {
		return fmt.Errorf("%w: portal account actor needs an id or a label", ErrInvalidEntry)
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	if entry.ID == "" {
		id, err := recorder.newID()
		if err != nil {
			return fmt.Errorf("generate audit id: %w", err)
		}
		entry.ID = id
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = recorder.now().UTC()
	}
	return recorder.repository.InsertAuditLog(ctx, entry)
}

func validActorKind(value string) bool {
	switch value {
	case ActorPortalAccount, ActorInternalApp, ActorSystem:
		return true
	default:
		return false
	}
}

func validOutcome(value string) bool {
	switch value {
	case OutcomeAccepted, OutcomeSucceeded, OutcomeRejected, OutcomeFailed:
		return true
	default:
		return false
	}
}
