package activation

import (
	"context"
	"testing"
)

func TestMemoryRepositoryDoesNotExposeActivationCode(t *testing.T) {
	t.Parallel()

	service := NewService()
	created, err := service.Create(context.Background(), testCreateInput(MethodBLE))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	observed, err := service.GetForInternalApp(context.Background(), "livpet-staging", created.ID)
	if err != nil {
		t.Fatalf("GetForInternalApp() error = %v", err)
	}
	if observed.Code != "" {
		t.Fatal("repository-backed read exposed the activation code")
	}
}
