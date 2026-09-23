package platformapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
)

// 项目 webhook 的 HTTP 契约：secret 只在创建响应出现一次，列表绝不含它。
func TestDeveloperWebhookRoutesKeepTheSecretOutOfListings(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	token := fixture.login(t, developerOneEmail)

	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"Hooked"}`, token)
	projectID := decodeJSON[struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}](t, created).Project.ID

	issued := fixture.call(t, http.MethodPost, "/api/v1/developer/projects/"+projectID+"/webhooks",
		`{"callback_url":"https://hooks.example.com/events","event_types":["device.event","ota.status"]}`, token)
	if issued.Code != http.StatusCreated {
		t.Fatalf("create webhook status = %d body = %s", issued.Code, issued.Body.String())
	}
	payload := decodeJSON[struct {
		Webhook struct {
			ID          string   `json:"id"`
			CallbackURL string   `json:"callback_url"`
			EventTypes  []string `json:"event_types"`
			Status      string   `json:"status"`
		} `json:"webhook"`
		WebhookSecret string `json:"webhook_secret"`
	}](t, issued)
	if payload.WebhookSecret == "" || payload.Webhook.ID == "" {
		t.Fatal("create webhook did not return the one-time secret")
	}
	if len(payload.Webhook.EventTypes) != 2 || payload.Webhook.Status != "active" {
		t.Fatalf("webhook projection = %+v", payload.Webhook)
	}

	listed := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/webhooks", "", token)
	if listed.Code != http.StatusOK {
		t.Fatalf("list webhooks status = %d body = %s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), payload.WebhookSecret) ||
		strings.Contains(listed.Body.String(), "webhook_secret") {
		t.Fatalf("webhook listing leaked the signing secret: %s", listed.Body.String())
	}

	// 非 https 或带查询串的回调必须在入口被拒绝。
	for name, body := range map[string]string{
		"http":    `{"callback_url":"http://hooks.example.com/events","event_types":["device.event"]}`,
		"query":   `{"callback_url":"https://hooks.example.com/events?token=1","event_types":["device.event"]}`,
		"no-type": `{"callback_url":"https://hooks.example.com/events"}`,
	} {
		recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/projects/"+projectID+"/webhooks", body, token)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("create webhook (%s) status = %d, want 400", name, recorder.Code)
		}
	}

	if disabled := fixture.call(t, http.MethodPatch, "/api/v1/developer/webhooks/"+payload.Webhook.ID,
		`{"status":"disabled"}`, token); disabled.Code != http.StatusOK {
		t.Fatalf("patch webhook status = %d body = %s", disabled.Code, disabled.Body.String())
	}
	if deleted := fixture.call(t, http.MethodDelete, "/api/v1/developer/webhooks/"+payload.Webhook.ID, "", token); deleted.Code != http.StatusOK {
		t.Fatalf("delete webhook status = %d body = %s", deleted.Code, deleted.Body.String())
	}
	if missing := fixture.call(t, http.MethodDelete, "/api/v1/developer/webhooks/"+payload.Webhook.ID, "", token); missing.Code != http.StatusNotFound {
		t.Fatalf("delete missing webhook status = %d, want 404", missing.Code)
	}
	// 跨开发者必须看不到：按不存在处理。
	second := fixture.login(t, developerTwoEmail)
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/webhooks", "", second); recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-developer webhook list status = %d, want 404", recorder.Code)
	}
}

func TestDeveloperWebhookDeliveryRoutesListAndFilterRecords(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	token := fixture.login(t, developerOneEmail)

	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"Delivery History"}`, token)
	projectID := decodeJSON[struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}](t, created).Project.ID

	deliveredStatusCode, deadStatusCode := 200, 503
	fixture.ops.webhookDeliveries = []operations.WebhookDelivery{
		{
			ID: "outbox-delivered", DeliveryID: "delivery-ok", EventType: "feeding_done",
			DeliveryStatus: "delivered", OutboxStatus: "delivered", Attempts: 1,
			StatusCode: &deliveredStatusCode, CreatedAt: time.Date(2026, 9, 23, 1, 2, 3, 0, time.UTC),
		},
		{
			ID: "outbox-dead", DeliveryID: "delivery-dead", EventType: "motion_detected",
			DeliveryStatus: "undelivered", OutboxStatus: "dead", Attempts: 8,
			StatusCode: &deadStatusCode, ErrorCode: "http_status",
			CreatedAt: time.Date(2026, 9, 23, 1, 1, 3, 0, time.UTC),
		},
	}

	listed := fixture.call(t, http.MethodGet,
		"/api/v1/developer/projects/"+projectID+"/webhooks/deliveries?status=undelivered", "", token)
	if listed.Code != http.StatusOK {
		t.Fatalf("list webhook deliveries status = %d body = %s", listed.Code, listed.Body.String())
	}
	payload := decodeJSON[struct {
		Items []struct {
			DeliveryID     string `json:"delivery_id"`
			DeliveryStatus string `json:"delivery_status"`
			OutboxStatus   string `json:"outbox_status"`
		} `json:"items"`
	}](t, listed)
	if len(payload.Items) != 1 || payload.Items[0].DeliveryID != "delivery-dead" ||
		payload.Items[0].DeliveryStatus != "undelivered" || payload.Items[0].OutboxStatus != "dead" {
		t.Fatalf("webhook delivery payload = %+v", payload.Items)
	}
	if fixture.ops.lastWebhookDelivery.ProjectID != projectID || fixture.ops.lastWebhookDelivery.Status != "undelivered" {
		t.Fatalf("webhook delivery filter = %+v", fixture.ops.lastWebhookDelivery)
	}
	if invalid := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/webhooks/deliveries?status=broken", "", token); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid webhook delivery status = %d body = %s", invalid.Code, invalid.Body.String())
	}
	second := fixture.login(t, developerTwoEmail)
	if forbidden := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/webhooks/deliveries", "", second); forbidden.Code != http.StatusNotFound {
		t.Fatalf("cross-developer webhook delivery list status = %d, want 404", forbidden.Code)
	}
}
