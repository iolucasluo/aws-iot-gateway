package audit

import (
	"context"
	"sync"
)

// MemoryRepository 供单元测试与非 DB 环境使用。
type MemoryRepository struct {
	mutex   sync.Mutex
	entries []Entry
}

func NewMemoryRepository() *MemoryRepository { return &MemoryRepository{} }

func (repository *MemoryRepository) InsertAuditLog(_ context.Context, entry Entry) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.entries = append(repository.entries, entry)
	return nil
}

// Entries 返回已记录事实的副本，供测试断言。
func (repository *MemoryRepository) Entries() []Entry {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	return append([]Entry{}, repository.entries...)
}
