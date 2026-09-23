package platformapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

// webhookResponse 永远不含 secret：明文只在创建响应出现一次。
type webhookResponse struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	CallbackURL string   `json:"callback_url"`
	EventTypes  []string `json:"event_types"`
	Status      string   `json:"status"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func newWebhookResponse(webhook project.Webhook) webhookResponse {
	return webhookResponse{
		ID: webhook.ID, ProjectID: webhook.ProjectID, CallbackURL: webhook.CallbackURL,
		EventTypes: append([]string{}, webhook.EventTypes...), Status: webhook.Status,
		CreatedAt: webhook.CreatedAt.UTC().Format(timeLayout),
		UpdatedAt: webhook.UpdatedAt.UTC().Format(timeLayout),
	}
}

func (server *Server) listProjectWebhooks(writer http.ResponseWriter, request *http.Request, projectID string) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		webhooks, err := server.projects().ListWebhooks(ctx, actor, projectID)
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]webhookResponse, 0, len(webhooks))
		for _, webhook := range webhooks {
			items = append(items, newWebhookResponse(webhook))
		}
		writePage(writer, items, "")
	})
}

func (server *Server) createProjectWebhook(writer http.ResponseWriter, request *http.Request, projectID string) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		var body struct {
			CallbackURL string   `json:"callback_url"`
			EventTypes  []string `json:"event_types"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		result, err := server.projects().CreateWebhook(ctx, actor, projectID, project.CreateWebhookInput{
			CallbackURL: body.CallbackURL, EventTypes: body.EventTypes,
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{
			"webhook": newWebhookResponse(result.Webhook),
			// 签名 secret 只在这里出现一次。
			"webhook_secret": result.Secret,
		})
	})
}

func (server *Server) serveDeveloperWebhook(writer http.ResponseWriter, request *http.Request, webhookID string) {
	if webhookID == "" || strings.Contains(webhookID, "/") {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	switch request.Method {
	case http.MethodPatch:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			var body struct {
				CallbackURL *string   `json:"callback_url"`
				EventTypes  *[]string `json:"event_types"`
				Status      *string   `json:"status"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			webhook, err := server.projects().UpdateWebhook(ctx, actor, webhookID, project.WebhookPatch{
				CallbackURL: body.CallbackURL, EventTypes: body.EventTypes, Status: body.Status,
			})
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"webhook": newWebhookResponse(webhook)})
		})
	case http.MethodDelete:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			if err := server.projects().DeleteWebhook(ctx, actor, webhookID); err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]string{"status": "deleted"})
		})
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

var _ = portalaccount.AudienceDeveloper

type webhookDeliveryResponse struct {
	ID             string `json:"id"`
	DeliveryID     string `json:"delivery_id"`
	EventType      string `json:"event_type"`
	DeliveryStatus string `json:"delivery_status"`
	OutboxStatus   string `json:"outbox_status"`
	Attempts       int    `json:"attempts"`
	StatusCode     *int   `json:"status_code,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	CreatedAt      string `json:"created_at"`
	LastAttemptAt  string `json:"last_attempt_at,omitempty"`
}

func newWebhookDeliveryResponse(value operations.WebhookDelivery) webhookDeliveryResponse {
	response := webhookDeliveryResponse{
		ID: value.ID, DeliveryID: value.DeliveryID, EventType: value.EventType,
		DeliveryStatus: value.DeliveryStatus, OutboxStatus: value.OutboxStatus,
		Attempts: value.Attempts, StatusCode: value.StatusCode, ErrorCode: value.ErrorCode,
		CreatedAt: value.CreatedAt.UTC().Format(timeLayout),
	}
	if value.LastAttemptAt != nil {
		response.LastAttemptAt = value.LastAttemptAt.UTC().Format(timeLayout)
	}
	return response
}

func (server *Server) listProjectWebhookDeliveries(writer http.ResponseWriter, request *http.Request, projectID string) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	status := strings.TrimSpace(request.URL.Query().Get("status"))
	if status != "" && status != "delivered" && status != "undelivered" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "status must be delivered or undelivered")
		return
	}
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		// 先经项目域做 owner/scope 校验，再查询与项目主体绑定的 outbox。
		if _, err := server.projects().ListWebhooks(ctx, actor, projectID); err != nil {
			writeProjectError(writer, err)
			return
		}
		values, err := server.operations().ListWebhookDeliveries(ctx, operations.WebhookDeliveryFilter{
			ProjectID: projectID, Status: status, Page: page,
		})
		if err != nil {
			if errors.Is(err, operations.ErrInvalidQuery) {
				writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
				return
			}
			writeOperationsError(writer, err)
			return
		}
		items := make([]webhookDeliveryResponse, 0, len(values))
		for _, value := range values {
			items = append(items, newWebhookDeliveryResponse(value))
		}
		cursor := ""
		if last := len(values) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(values), values[last].CreatedAt, values[last].ID)
		}
		writePage(writer, items, cursor)
	})
}
