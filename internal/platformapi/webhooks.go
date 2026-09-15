package platformapi

import (
	"context"
	"net/http"
	"strings"

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
