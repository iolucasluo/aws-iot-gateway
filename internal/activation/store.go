package activation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type StoredRecord struct {
	Activation   Activation
	CodeHash     string
	CSRPEM       string
	Installation Installation
}

type RecordStore interface {
	Save(context.Context, []StoredRecord) error
	Load(context.Context) ([]StoredRecord, error)
}

type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Save(ctx context.Context, records []StoredRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path == "" {
		return fmt.Errorf("activation store path is empty")
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("marshal activation records: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create activation state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".activations-*")
	if err != nil {
		return fmt.Errorf("create activation state file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect activation state file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write activation state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync activation state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close activation state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace activation state: %w", err)
	}
	return nil
}

func (s *FileStore) Load(ctx context.Context) ([]StoredRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.path == "" {
		return nil, fmt.Errorf("activation store path is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return []StoredRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read activation state: %w", err)
	}
	var records []StoredRecord
	if err := json.Unmarshal(encoded, &records); err != nil {
		return nil, fmt.Errorf("decode activation state: %w", err)
	}
	for index := range records {
		records[index].Activation.Code = ""
	}
	return records, nil
}
