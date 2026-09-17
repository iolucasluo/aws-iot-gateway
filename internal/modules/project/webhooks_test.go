package project

import (
	"context"
	"errors"
	"testing"
)

func TestCreateWebhookValidatesCallbackAndEventTypes(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")
	other := developerActor("10000000-0000-0000-0000-0000000000a2")
	created, err := service.CreateProject(ctx, owner, CreateProjectInput{Type: TypeCloud, Name: "Hooked"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	issued, err := service.CreateWebhook(ctx, owner, created.ID, CreateWebhookInput{
		CallbackURL: " https://hooks.example.com/events ",
		EventTypes:  []string{"device.event", "device.event", " ota.status "},
	})
	if err != nil {
		t.Fatalf("CreateWebhook() error = %v", err)
	}
	if issued.Secret == "" || issued.Webhook.CallbackURL != "https://hooks.example.com/events" {
		t.Fatalf("webhook issue = %+v", issued)
	}
	if len(issued.Webhook.EventTypes) != 2 {
		t.Fatalf("event types = %v, want deduplicated and trimmed", issued.Webhook.EventTypes)
	}
	if string(issued.Webhook.SecretCiphertext) == issued.Secret {
		t.Fatal("stored ciphertext equals the plaintext secret")
	}
	opened, err := service.OpenWebhookSecret(issued.Webhook)
	if err != nil || opened != issued.Secret {
		t.Fatalf("OpenWebhookSecret() = %q, err = %v", opened, err)
	}

	for name, input := range map[string]CreateWebhookInput{
		"http-scheme":    {CallbackURL: "http://hooks.example.com/events", EventTypes: []string{"device.event"}},
		"empty-url":      {CallbackURL: "  ", EventTypes: []string{"device.event"}},
		"query-string":   {CallbackURL: "https://hooks.example.com/events?token=1", EventTypes: []string{"device.event"}},
		"fragment":       {CallbackURL: "https://hooks.example.com/events#frag", EventTypes: []string{"device.event"}},
		"credentials":    {CallbackURL: "https://user:pass@hooks.example.com/events", EventTypes: []string{"device.event"}},
		"no-event-types": {CallbackURL: "https://hooks.example.com/events"},
		"bad-event-type": {CallbackURL: "https://hooks.example.com/events", EventTypes: []string{"Device Event"}},
	} {
		if _, err := service.CreateWebhook(ctx, owner, created.ID, input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("CreateWebhook(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}
	if _, err := service.CreateWebhook(ctx, other, created.ID, CreateWebhookInput{
		CallbackURL: "https://hooks.example.com/events", EventTypes: []string{"device.event"},
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateWebhook(other developer) error = %v, want ErrForbidden", err)
	}
	// 一个项目只能有一个 active 回调：第二次创建被拒绝。
	if _, err := service.CreateWebhook(ctx, owner, created.ID, CreateWebhookInput{
		CallbackURL: "https://hooks.example.com/second", EventTypes: []string{"device.event"},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateWebhook(second active) error = %v, want ErrInvalidInput", err)
	}
}

func TestWebhookUpdateDeleteAndResolution(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")
	other := developerActor("10000000-0000-0000-0000-0000000000a2")
	created, err := service.CreateProject(ctx, owner, CreateProjectInput{Type: TypeCloud, Name: "Hooked"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	issued, err := service.CreateWebhook(ctx, owner, created.ID, CreateWebhookInput{
		CallbackURL: "https://hooks.example.com/events", EventTypes: []string{"device.event"},
	})
	if err != nil {
		t.Fatalf("CreateWebhook() error = %v", err)
	}

	// 投递器按项目解析目标。
	resolved, err := service.ResolveActiveWebhook(ctx, created.ID)
	if err != nil || resolved.ID != issued.Webhook.ID {
		t.Fatalf("ResolveActiveWebhook() = %+v, err = %v", resolved, err)
	}
	// 项目不存在与"没有 active 回调"对投递器是同一件事：回退静态配置。
	if _, err := service.ResolveActiveWebhook(ctx, "50000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("ResolveActiveWebhook(unknown project) error = %v, want ErrWebhookNotFound", err)
	}

	if _, err := service.UpdateWebhook(ctx, other, issued.Webhook.ID, WebhookPatch{Status: stringPointer(StatusDisabled)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateWebhook(other developer) error = %v, want ErrForbidden", err)
	}
	if _, err := service.UpdateWebhook(ctx, owner, issued.Webhook.ID, WebhookPatch{Status: stringPointer("paused")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateWebhook(unknown status) error = %v, want ErrInvalidInput", err)
	}
	disabled, err := service.UpdateWebhook(ctx, owner, issued.Webhook.ID, WebhookPatch{Status: stringPointer(StatusDisabled)})
	if err != nil || disabled.Status != StatusDisabled {
		t.Fatalf("UpdateWebhook(disable) = %+v, err = %v", disabled, err)
	}
	// 停用后不再解析为投递目标：投递器回退静态配置。
	if _, err := service.ResolveActiveWebhook(ctx, created.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("ResolveActiveWebhook(disabled) error = %v, want ErrWebhookNotFound", err)
	}
	// 停用项目后同样不再投递。
	if _, err := service.UpdateWebhook(ctx, owner, issued.Webhook.ID, WebhookPatch{Status: stringPointer(StatusActive)}); err != nil {
		t.Fatalf("UpdateWebhook(enable) error = %v", err)
	}
	if _, err := service.UpdateProject(ctx, owner, created.ID, ProjectPatch{Status: stringPointer(StatusDisabled)}); err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if _, err := service.ResolveActiveWebhook(ctx, created.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("ResolveActiveWebhook(disabled project) error = %v, want ErrWebhookNotFound", err)
	}
	if _, err := service.UpdateProject(ctx, owner, created.ID, ProjectPatch{Status: stringPointer(StatusActive)}); err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}

	webhooks, err := service.ListWebhooks(ctx, owner, created.ID)
	if err != nil || len(webhooks) != 1 {
		t.Fatalf("ListWebhooks() = %d, err = %v; want 1", len(webhooks), err)
	}
	if _, err := service.ListWebhooks(ctx, other, created.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("ListWebhooks(other developer) error = %v, want ErrProjectNotFound", err)
	}
	if err := service.DeleteWebhook(ctx, other, issued.Webhook.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("DeleteWebhook(other developer) error = %v, want ErrForbidden", err)
	}
	if err := service.DeleteWebhook(ctx, owner, issued.Webhook.ID); err != nil {
		t.Fatalf("DeleteWebhook() error = %v", err)
	}
	if err := service.DeleteWebhook(ctx, owner, issued.Webhook.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("DeleteWebhook(again) error = %v, want ErrWebhookNotFound", err)
	}
}
