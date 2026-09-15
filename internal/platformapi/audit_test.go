package platformapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/audit"
)

func auditEntriesOf(t *testing.T, entries []audit.Entry, operation string) []audit.Entry {
	t.Helper()
	matched := make([]audit.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Operation == operation {
			matched = append(matched, entry)
		}
	}
	return matched
}

// 门户写操作必须留下带主体与结果的审计事实，读操作不审计。
func TestPortalWritesAreAuditedWithActorAndOutcome(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	token := fixture.login(t, developerOneEmail)

	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"Audited"}`, token)
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", created.Code, created.Body.String())
	}
	projectID := decodeJSON[struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}](t, created).Project.ID

	// 读操作不产生审计。
	fixture.call(t, http.MethodGet, "/api/v1/developer/projects", "", token)

	// 被拒绝的写操作也要留痕。
	fixture.call(t, http.MethodPatch, "/api/v1/developer/projects/"+projectID,
		`{"status":"archived"}`, token)
	fixture.call(t, http.MethodPost, "/api/v1/developer/projects/"+projectID+"/credentials",
		`{"name":"x","permissions":["devices:write"]}`, token)

	entries := fixture.audit.recorded()
	for _, entry := range entries {
		if entry.ActorKind != audit.ActorPortalAccount || entry.ActorLabel != developerOneEmail {
			t.Fatalf("audit entry lost its actor: %+v", entry)
		}
		if entry.RequestID == "" {
			t.Fatalf("audit entry lost its request id: %+v", entry)
		}
		if _, leaked := entry.Metadata["api_secret"]; leaked {
			t.Fatalf("audit metadata must not carry secrets: %+v", entry.Metadata)
		}
	}
	succeeded := auditEntriesOf(t, entries, "POST developer.projects")
	if len(succeeded) != 1 || succeeded[0].Outcome != audit.OutcomeSucceeded {
		t.Fatalf("project create audit = %+v, want one succeeded entry", succeeded)
	}
	if succeeded[0].ResourceType != "projects" {
		t.Fatalf("resource type = %q, want projects", succeeded[0].ResourceType)
	}
	// 创建与更新必须是不同的 operation，否则审计无法区分动作。
	updateRejected := auditEntriesOf(t, entries, "PATCH developer.projects")
	if len(updateRejected) != 1 || updateRejected[0].Outcome != audit.OutcomeRejected {
		t.Fatalf("rejected project patch audit = %+v, want one rejected entry", updateRejected)
	}
	if updateRejected[0].ResourceID != projectID {
		t.Fatalf("resource id = %q, want the project id", updateRejected[0].ResourceID)
	}
	rejected := auditEntriesOf(t, entries, "POST developer.projects.credentials")
	if len(rejected) != 1 || rejected[0].Outcome != audit.OutcomeRejected {
		t.Fatalf("rejected credential write audit = %+v, want one rejected entry", rejected)
	}
	if len(auditEntriesOf(t, entries, "GET developer.projects")) != 0 && len(entries) > 4 {
		t.Fatalf("reads must not be audited: %+v", entries)
	}
}

func TestLoginAuditRecordsSuccessAndFailureWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	fixture.login(t, developerOneEmail)
	fixture.call(t, http.MethodPost, "/api/v1/developer/auth/login",
		`{"email":"`+developerOneEmail+`","password":"wrong passphrase value"}`, "")

	entries := auditEntriesOf(t, fixture.audit.recorded(), "POST auth.login")
	if len(entries) != 2 {
		t.Fatalf("login audits = %d, want 2 (success + failure)", len(entries))
	}
	if entries[0].Outcome != audit.OutcomeSucceeded || entries[0].ActorLabel != developerOneEmail {
		t.Fatalf("success audit = %+v", entries[0])
	}
	if entries[1].Outcome != audit.OutcomeRejected {
		t.Fatalf("failure audit = %+v", entries[1])
	}
	for _, entry := range entries {
		encoded := entry.Operation + entry.ActorLabel + entry.ErrorCode
		if strings.Contains(encoded, "passphrase") || strings.Contains(encoded, "password") {
			t.Fatalf("audit entry leaked credential material: %+v", entry)
		}
	}
}

// 审计不可用时业务请求照常完成，但错误必须交给 AuditErrorHandler。
func TestAuditFailureDoesNotBreakTheRequest(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	token := fixture.login(t, developerOneEmail)
	fixture.audit.mutex.Lock()
	fixture.audit.fail = errors.New("postgres unavailable")
	fixture.audit.mutex.Unlock()

	var captured error
	fixture.server.options.AuditErrorHandler = func(err error) { captured = err }
	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"Still Works"}`, token)
	if created.Code != http.StatusCreated {
		t.Fatalf("create project with a failing audit sink status = %d body = %s", created.Code, created.Body.String())
	}
	if captured == nil {
		t.Fatal("audit failure was swallowed without reaching AuditErrorHandler")
	}
	_ = context.Background()
}
