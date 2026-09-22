package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

// writeProjectError 把项目域错误稳定映射到 HTTP 语义。
// 越权与不存在都按 404 处理，避免用状态码泄露资源存在性。
func writeProjectError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrProjectNotFound),
		errors.Is(err, project.ErrProductNotFound),
		errors.Is(err, project.ErrProductNotInProject):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, project.ErrOwnerNotFound),
		errors.Is(err, project.ErrManufacturerNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, project.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", "operation is not allowed")
	case errors.Is(err, project.ErrCredentialNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "credential not found")
	case errors.Is(err, project.ErrWebhookNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "webhook not found")
	case errors.Is(err, project.ErrDuplicateProduct):
		writeError(writer, http.StatusConflict, "product_already_attached", "product is already attached to the project")
	case errors.Is(err, project.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

type projectResponse struct {
	ID             string `json:"id"`
	OwnerAccountID string `json:"owner_account_id"`
	ProjectType    string `json:"project_type"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	ManufacturerID string `json:"manufacturer_id,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func newProjectResponse(value project.Project) projectResponse {
	return projectResponse{
		ID: value.ID, OwnerAccountID: value.OwnerAccountID, ProjectType: string(value.Type),
		Name: value.Name, Description: value.Description, ManufacturerID: value.ManufacturerID,
		Status: value.Status, CreatedAt: value.CreatedAt.UTC().Format(timeLayout),
		UpdatedAt: value.UpdatedAt.UTC().Format(timeLayout),
	}
}

// credentialResponse 永远不包含 secret：密文留在库里，明文只在签发响应出现一次。
type credentialResponse struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Name        string   `json:"name"`
	APIKey      string   `json:"api_key"`
	Permissions []string `json:"permissions"`
	Status      string   `json:"status"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	LastUsedAt  string   `json:"last_used_at,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func newCredentialResponse(value project.Credential) credentialResponse {
	response := credentialResponse{
		ID: value.ID, ProjectID: value.ProjectID, Name: value.Name, APIKey: value.APIKey,
		Permissions: append([]string{}, value.Permissions...), Status: value.Status,
		CreatedAt: value.CreatedAt.UTC().Format(timeLayout), UpdatedAt: value.UpdatedAt.UTC().Format(timeLayout),
	}
	if value.ExpiresAt != nil {
		response.ExpiresAt = value.ExpiresAt.UTC().Format(timeLayout)
	}
	if value.LastUsedAt != nil {
		response.LastUsedAt = value.LastUsedAt.UTC().Format(timeLayout)
	}
	return response
}

type productResponse struct {
	ID             string   `json:"id"`
	PID            string   `json:"pid"`
	DPProfileCode  string   `json:"dp_profile_code"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	Capabilities   []string `json:"capabilities"`
	OwnerAccountID string   `json:"owner_account_id,omitempty"`
	Category       string   `json:"category,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

func newProductResponse(value project.Product) productResponse {
	return productResponse{
		ID: value.ID, PID: value.PID, DPProfileCode: value.DPProfileCode, Name: value.Name,
		Status: value.Status, Capabilities: append([]string{}, value.Capabilities...),
		OwnerAccountID: value.OwnerAccountID, Category: value.Category,
		CreatedAt: value.CreatedAt.UTC().Format(timeLayout), UpdatedAt: value.UpdatedAt.UTC().Format(timeLayout),
	}
}

// serveDeveloper 处理 developer 门户资源。会话面已在 servePortal 处理。
func (server *Server) serveDeveloper(writer http.ResponseWriter, request *http.Request, path string) {
	switch {
	case path == "projects" && request.Method == http.MethodGet:
		server.listProjects(writer, request)
	case path == "projects" && request.Method == http.MethodPost:
		server.createProject(writer, request)
	case path == "dp-profiles" && request.Method == http.MethodGet:
		server.listDPProfiles(writer, request)
	case strings.HasPrefix(path, "dp-profiles/") && strings.HasSuffix(path, "/definitions") && request.Method == http.MethodGet:
		code := strings.TrimSuffix(strings.TrimPrefix(path, "dp-profiles/"), "/definitions")
		server.listDPDefinitions(writer, request, code)
	case path == "dp-definitions" && request.Method == http.MethodPost:
		server.createDPDefinition(writer, request)
	case strings.HasPrefix(path, "dp-definitions/") && request.Method == http.MethodDelete:
		server.deleteDPDefinition(writer, request, strings.TrimPrefix(path, "dp-definitions/"))
	case path == "products" && request.Method == http.MethodGet:
		server.listProducts(writer, request)
	case path == "products" && request.Method == http.MethodPost:
		server.createProduct(writer, request)
	case path == "ota" || strings.HasPrefix(path, "ota/"):
		server.serveDeveloperOTA(writer, request, strings.TrimPrefix(path, "ota/"))
	case strings.HasPrefix(path, "devices/"):
		server.serveDeveloperDevice(writer, request, strings.TrimPrefix(path, "devices/"))
	default:
		switch {
		case strings.HasPrefix(path, "projects/"):
			server.serveDeveloperProject(writer, request, strings.TrimPrefix(path, "projects/"))
		case strings.HasPrefix(path, "credentials/"):
			server.serveDeveloperCredential(writer, request, strings.TrimPrefix(path, "credentials/"))
		case strings.HasPrefix(path, "webhooks/"):
			server.serveDeveloperWebhook(writer, request, strings.TrimPrefix(path, "webhooks/"))
		case strings.HasPrefix(path, "products/"):
			server.serveDeveloperProduct(writer, request, strings.TrimPrefix(path, "products/"))
		default:
			writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		}
	}
}

func (server *Server) serveDeveloperProject(writer http.ResponseWriter, request *http.Request, rest string) {
	projectID, sub, hasSub := strings.Cut(rest, "/")
	if projectID == "" {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	switch {
	case !hasSub && request.Method == http.MethodGet:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			value, err := server.projects().GetProject(ctx, actor, projectID)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"project": newProjectResponse(value)})
		})
	case !hasSub && request.Method == http.MethodPatch:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			var body struct {
				Name        *string `json:"name"`
				Description *string `json:"description"`
				Status      *string `json:"status"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			value, err := server.projects().UpdateProject(ctx, actor, projectID, project.ProjectPatch{
				Name: body.Name, Description: body.Description, Status: body.Status,
			})
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"project": newProjectResponse(value)})
		})
	case !hasSub && request.Method == http.MethodDelete:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			if err := server.projects().DeleteProject(ctx, actor, projectID); err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]string{"status": "deleted"})
		})
	case sub == "products" && request.Method == http.MethodGet:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			productIDs, pids, err := server.projects().ListProjectProducts(ctx, actor, projectID)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"product_ids": productIDs, "pids": pids})
		})
	case sub == "products" && request.Method == http.MethodPost:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			var body struct {
				ProductID string `json:"product_id"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			value, err := server.projects().AttachProduct(ctx, actor, projectID, body.ProductID)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"project": newProjectResponse(value)})
		})
	case strings.HasPrefix(sub, "products/"):
		productID := strings.TrimPrefix(sub, "products/")
		if productID == "" || strings.Contains(productID, "/") || request.Method != http.MethodDelete {
			writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
			return
		}
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			value, err := server.projects().DetachProduct(ctx, actor, projectID, productID)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"project": newProjectResponse(value)})
		})
	case sub == "devices" && request.Method == http.MethodGet:
		server.listDeveloperProjectDevices(writer, request, projectID)
	case sub == "webhooks" && request.Method == http.MethodGet:
		server.listProjectWebhooks(writer, request, projectID)
	case sub == "webhooks" && request.Method == http.MethodPost:
		server.createProjectWebhook(writer, request, projectID)
	case sub == "credentials" && request.Method == http.MethodGet:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			credentials, err := server.projects().ListCredentials(ctx, actor, projectID)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			items := make([]credentialResponse, 0, len(credentials))
			for _, credential := range credentials {
				items = append(items, newCredentialResponse(credential))
			}
			writeJSON(writer, http.StatusOK, map[string]any{"items": items})
		})
	case sub == "credentials" && request.Method == http.MethodPost:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			var body struct {
				Name        string   `json:"name"`
				Permissions []string `json:"permissions"`
				ExpiresAt   *string  `json:"expires_at"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			expiresAt, err := parseOptionalTimestamp(body.ExpiresAt)
			if err != nil {
				writeError(writer, http.StatusBadRequest, "invalid_request", "expires_at must be an RFC3339 timestamp")
				return
			}
			result, err := server.projects().CreateCredential(ctx, actor, projectID, project.CreateCredentialInput{
				Name: body.Name, Permissions: body.Permissions, ExpiresAt: expiresAt,
			})
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusCreated, map[string]any{
				"credential": newCredentialResponse(result.Credential),
				"api_secret": result.APISecret,
			})
		})
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (server *Server) serveDeveloperCredential(writer http.ResponseWriter, request *http.Request, rest string) {
	credentialID, action, found := strings.Cut(rest, "/")
	if !found || credentialID == "" || request.Method != http.MethodPost {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		switch action {
		case "rotate":
			result, err := server.projects().RotateCredential(ctx, actor, credentialID)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{
				"credential": newCredentialResponse(result.Credential),
				"api_secret": result.APISecret,
			})
		case "disable":
			value, err := server.projects().DisableCredential(ctx, actor, credentialID)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"credential": newCredentialResponse(value)})
		default:
			writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		}
	})
}

func (server *Server) serveDeveloperProduct(writer http.ResponseWriter, request *http.Request, pid string) {
	if pid == "" || strings.Contains(pid, "/") {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			value, err := server.projects().GetProduct(ctx, actor, pid)
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"product": newProductResponse(value)})
		})
	case http.MethodPatch:
		server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
			var body struct {
				Name         *string   `json:"name"`
				Status       *string   `json:"status"`
				Capabilities *[]string `json:"capabilities"`
				Category     *string   `json:"category"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			value, err := server.projects().UpdateProduct(ctx, actor, pid, project.ProductPatch{
				Name: body.Name, Status: body.Status, Capabilities: body.Capabilities, Category: body.Category,
			})
			if err != nil {
				writeProjectError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"product": newProductResponse(value)})
		})
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (server *Server) listProjects(writer http.ResponseWriter, request *http.Request) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		values, err := server.projects().ListProjects(ctx, actor, project.ProjectFilter{
			Type:   project.ProjectType(strings.TrimSpace(request.URL.Query().Get("project_type"))),
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]projectResponse, 0, len(values))
		for _, value := range values {
			items = append(items, newProjectResponse(value))
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	})
}

func (server *Server) createProject(writer http.ResponseWriter, request *http.Request) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		var body struct {
			ProjectType    string `json:"project_type"`
			Name           string `json:"name"`
			Description    string `json:"description"`
			ManufacturerID string `json:"manufacturer_id"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		value, err := server.projects().CreateProject(ctx, actor, project.CreateProjectInput{
			Type: project.ProjectType(strings.TrimSpace(body.ProjectType)), Name: body.Name,
			Description: body.Description, ManufacturerID: body.ManufacturerID,
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{"project": newProjectResponse(value)})
	})
}

func (server *Server) listDPProfiles(writer http.ResponseWriter, request *http.Request) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, _ project.Actor) {
		profiles, err := server.projects().ListActiveDPProfiles(ctx)
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]map[string]string, 0, len(profiles))
		for _, profile := range profiles {
			items = append(items, map[string]string{"code": profile.Code, "name": profile.Name})
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	})
}

func (server *Server) listDPDefinitions(writer http.ResponseWriter, request *http.Request, profileCode string) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		defs, err := server.projects().ListDPDefinitionsForProfile(ctx, actor, profileCode)
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]map[string]any, 0, len(defs))
		for _, d := range defs {
			item := map[string]any{
				"id": d.ID, "code": d.Code, "type": d.Type, "direction": d.Direction,
				"owner_account_id": d.OwnerAccountID,
				"created_at": d.CreatedAt.UTC().Format(timeLayout),
			}
			if len(d.Spec) > 0 {
				var spec any
				if json.Unmarshal(d.Spec, &spec) == nil {
					item["spec"] = spec
				}
			}
			items = append(items, item)
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	})
}

func (server *Server) createDPDefinition(writer http.ResponseWriter, request *http.Request) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		var body struct {
			Code           string          `json:"code"`
			Type           string          `json:"type"`
			Direction      string          `json:"direction"`
			Spec           json.RawMessage `json:"spec"`
			DPProfileCodes []string        `json:"dp_profile_codes"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		def, err := server.projects().CreateDPDefinition(ctx, actor, project.CreateDPDefinitionInput{
			Code: body.Code, Type: body.Type, Direction: body.Direction,
			Spec: body.Spec, DPProfileCodes: body.DPProfileCodes,
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{"dp_definition": map[string]any{
			"id": def.ID, "code": def.Code, "type": def.Type, "direction": def.Direction,
			"owner_account_id": def.OwnerAccountID,
			"created_at": def.CreatedAt.UTC().Format(timeLayout),
		}})
	})
}

func (server *Server) deleteDPDefinition(writer http.ResponseWriter, request *http.Request, id string) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		if err := server.projects().DeleteDPDefinition(ctx, actor, id); err != nil {
			writeProjectError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "deleted"})
	})
}

func (server *Server) listProducts(writer http.ResponseWriter, request *http.Request) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		values, err := server.projects().ListProducts(ctx, actor, project.ProductFilter{
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
			PID:    strings.TrimSpace(request.URL.Query().Get("pid")),
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]productResponse, 0, len(values))
		for _, value := range values {
			items = append(items, newProductResponse(value))
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	})
}

func (server *Server) createProduct(writer http.ResponseWriter, request *http.Request) {
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		var body struct {
			PID           string   `json:"pid"`
			Name          string   `json:"name"`
			DPProfileCode string   `json:"dp_profile_code"`
			Capabilities  []string `json:"capabilities"`
			Category      string   `json:"category"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		value, err := server.projects().CreateProduct(ctx, actor, project.CreateProductInput{
			PID: body.PID, Name: body.Name, DPProfileCode: body.DPProfileCode,
			Capabilities: body.Capabilities, Category: body.Category,
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{"product": newProductResponse(value)})
	})
}

func (server *Server) withDeveloperActor(
	writer http.ResponseWriter, request *http.Request,
	handle func(ctx context.Context, actor project.Actor),
) {
	server.authenticated(writer, request, portalaccount.AudienceDeveloper, func(ctx context.Context, account portalaccount.Account, _ portalaccount.Session) {
		handle(ctx, portalaccount.ActorFromAccount(account))
	})
}

func parseOptionalTimestamp(value *string) (*time.Time, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*value))
	if err != nil {
		return nil, err
	}
	utc := parsed.UTC()
	return &utc, nil
}
