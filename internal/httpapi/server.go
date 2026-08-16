package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
	devicemodule "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type server struct {
	activation    *activation.Service
	binding       *binding.Service
	device        *devicemodule.Service
	events        *events.QueryService
	media         *media.StreamService
	assets        *media.AssetService
	verifier      *internalauth.Verifier
	timeBootstrap TimeBootstrapService
	bootstrapRate TimeBootstrapLimiter
	deviceRuntime DeviceRuntimeConfig
}

type DeviceRuntimeConfig struct {
	AWSRegion          string
	CredentialEndpoint string
	RoleAlias          string
	TopicPrefix        string
	ShadowName         string
	KVSChannelPrefix   string
}

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Dependencies struct {
	Binding              *binding.Service
	Device               *devicemodule.Service
	Events               *events.QueryService
	Media                *media.StreamService
	Assets               *media.AssetService
	TimeBootstrap        TimeBootstrapService
	TimeBootstrapLimiter TimeBootstrapLimiter
	DeviceRuntime        DeviceRuntimeConfig
}

func NewInternalAPI(service *activation.Service, verifier *internalauth.Verifier, dependencies ...Dependencies) http.Handler {
	var dependency Dependencies
	if len(dependencies) > 0 {
		dependency = dependencies[0]
	}
	return server{
		activation: service, binding: dependency.Binding, device: dependency.Device,
		events: dependency.Events, media: dependency.Media, assets: dependency.Assets, verifier: verifier,
		timeBootstrap: dependency.TimeBootstrap, bootstrapRate: dependency.TimeBootstrapLimiter,
		deviceRuntime: dependency.DeviceRuntime,
	}
}

func (s server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/api/v1/device-time/bootstrap" {
		s.serveTimeBootstrap(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/v1/openapi/") {
		HMACMiddleware(s.verifier, http.HandlerFunc(s.serveInternalApp)).ServeHTTP(writer, request)
		return
	}
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/device-activations/session/prepare-installation":
		s.prepareDevice(writer, request, "")
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/device-activations/qr/prepare-installation":
		s.prepareDevice(writer, request, activation.MethodQR)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/device-activations/confirm-installation":
		s.confirmDevice(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (s server) serveInternalApp(writer http.ResponseWriter, request *http.Request) {
	if s.serveDeviceOperation(writer, request) {
		return
	}
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/openapi/device-activations":
		s.createActivation(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/openapi/devices/list":
		s.listDevices(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/openapi/devices/token":
		s.issueDeviceToken(writer, request)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/v1/openapi/devices/unbind/"):
		s.unbindDevice(writer, request, strings.TrimPrefix(request.URL.Path, "/api/v1/openapi/devices/unbind/"))
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/v1/openapi/devices/recommission/"):
		s.recommissionDevice(writer, request, strings.TrimPrefix(request.URL.Path, "/api/v1/openapi/devices/recommission/"))
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/v1/openapi/device-activations/") && strings.HasSuffix(request.URL.Path, "/sessions"):
		activationID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/api/v1/openapi/device-activations/"), "/sessions")
		if activationID == "" || strings.Contains(activationID, "/") {
			writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
			return
		}
		s.createActivationSession(writer, request, activationID)
	case request.Method == http.MethodGet:
		activationID, ok := strings.CutPrefix(request.URL.Path, "/api/v1/openapi/device-activations/")
		if ok && activationID != "" && !strings.Contains(activationID, "/") {
			s.getActivation(writer, request, activationID)
			return
		}
		fallthrough
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (s server) serveDeviceOperation(writer http.ResponseWriter, request *http.Request) bool {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/openapi/devices/")
	if path == request.URL.Path || path == "" {
		return false
	}
	parts := strings.Split(path, "/")
	if parts[0] == "" || len(parts) > 3 {
		return false
	}
	operation := "detail"
	if len(parts) >= 2 {
		operation = parts[1]
	}
	recognized := (operation == "detail" && request.Method == http.MethodGet) ||
		(operation == "shadow" && request.Method == http.MethodGet) ||
		(operation == "commands" && request.Method == http.MethodPost) ||
		(operation == "schedules" && request.Method == http.MethodPut) ||
		(operation == "events" && request.Method == http.MethodGet) ||
		(operation == "streams" && len(parts) == 2 && request.Method == http.MethodPost) ||
		(operation == "streams" && len(parts) == 3 && request.Method == http.MethodDelete) ||
		(operation == "snapshots" && len(parts) == 2 && request.Method == http.MethodPost) ||
		(operation == "snapshots" && len(parts) == 3 && request.Method == http.MethodGet) ||
		(operation == "recordings" && len(parts) == 2 && (request.Method == http.MethodPost || request.Method == http.MethodGet)) ||
		(operation == "recordings" && len(parts) == 3 && request.Method == http.MethodGet) ||
		(operation == "feed-audio" && len(parts) == 2 && (request.Method == http.MethodPost || request.Method == http.MethodDelete)) ||
		(operation == "network-reconfiguration-authorizations" && len(parts) == 2 && request.Method == http.MethodPost)
	if !recognized {
		return false
	}
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return true
	}
	if s.binding == nil {
		writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "device service is unavailable")
		return true
	}
	deviceToken := request.Header.Get("X-Device-Token")
	if strings.TrimSpace(deviceToken) == "" {
		writeError(writer, http.StatusUnauthorized, "missing_device_token", "X-Device-Token is required")
		return true
	}
	ownedBinding, err := s.binding.AuthorizeDevice(request.Context(), principal.InternalAppID, deviceToken, parts[0])
	if err != nil {
		writeBindingError(writer, err)
		return true
	}
	switch operation {
	case "network-reconfiguration-authorizations":
		var body struct {
			BindingExternalUserID string `json:"binding_external_user_id"`
			WiFiCredentialsSHA256 string `json:"wifi_credentials_sha256"`
		}
		if !decodeJSON(writer, request, &body) {
			return true
		}
		if body.BindingExternalUserID == "" ||
			body.BindingExternalUserID != ownedBinding.BindingExternalUserID {
			writeError(writer, http.StatusForbidden, "device_operation_forbidden", "device operation is forbidden")
			return true
		}
		if !sha256HexPattern.MatchString(body.WiFiCredentialsSHA256) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "Wi-Fi credentials digest is invalid")
			return true
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"authorized": true, "device_sn": ownedBinding.DeviceSN,
		})
		return true
	case "detail":
		writeJSON(writer, http.StatusOK, map[string]any{
			"device_sn": ownedBinding.DeviceSN, "pid": "feeder-x1", "status": "provisioned",
			"binding_external_user_id": ownedBinding.BindingExternalUserID,
		})
		return true
	case "shadow":
		if s.device == nil {
			writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "device service is unavailable")
			return true
		}
		shadow, err := s.device.GetShadow(request.Context(), ownedBinding.DeviceSN)
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "device shadow is unavailable")
			return true
		}
		writeJSON(writer, http.StatusOK, shadow)
		return true
	case "events":
		if s.events == nil {
			writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "device event service is unavailable")
			return true
		}
		limit := 50
		if rawLimit := request.URL.Query().Get("limit"); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(writer, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
				return true
			}
			limit = parsed
		}
		items, err := s.events.List(request.Context(), ownedBinding.DeviceID, request.URL.Query().Get("type"), limit)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error", "device events could not be loaded")
			return true
		}
		writeJSON(writer, http.StatusOK, map[string]any{"events": items})
		return true
	case "streams":
		if s.media == nil {
			writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "media service is unavailable")
			return true
		}
		if request.Method == http.MethodDelete {
			if err := s.media.Delete(
				request.Context(), principal.InternalAppID, ownedBinding.DeviceID,
				ownedBinding.ID, ownedBinding.DeviceSN, parts[2],
			); err != nil {
				writeMediaError(writer, err)
				return true
			}
			writeJSON(writer, http.StatusOK, map[string]any{"stream_id": parts[2], "closed": true})
			return true
		}
		var body struct {
			Type                  string `json:"type"`
			WaitACK               *bool  `json:"wait_ack,omitempty"`
			RequestedCapabilities *struct {
				VideoReceive *bool     `json:"video_receive"`
				AudioReceive *bool     `json:"audio_receive"`
				AudioSend    *bool     `json:"audio_send"`
				DataChannels *[]string `json:"data_channels"`
			} `json:"requested_capabilities"`
		}
		if !decodeJSON(writer, request, &body) {
			return true
		}
		capabilities := body.RequestedCapabilities
		if body.Type != "webrtc" || (body.WaitACK != nil && !*body.WaitACK) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "WebRTC stream requires wait_ack")
			return true
		}
		legacyLivPetStream := capabilities == nil
		requested := media.StreamCapabilities{}
		if capabilities == nil {
			if body.WaitACK == nil {
				writeError(writer, http.StatusBadRequest, "invalid_stream_capabilities", "legacy WebRTC stream requires explicit wait_ack")
				return true
			}
			// LivPet's existing viewer always offers recvonly video and sendrecv audio,
			// but predates the explicit webrtc.v1 capability request. Keep that exact
			// wire shape compatible while still requiring the device's real ACK/grant.
			requested = media.StreamCapabilities{
				VideoReceive: true, AudioReceive: true, AudioSend: true, DataChannels: []string{},
			}
		} else if capabilities.VideoReceive == nil || capabilities.AudioReceive == nil ||
			capabilities.AudioSend == nil || capabilities.DataChannels == nil {
			writeError(writer, http.StatusBadRequest, "invalid_stream_capabilities", "requested_capabilities must contain all fields")
			return true
		} else {
			requested = media.StreamCapabilities{
				VideoReceive: *capabilities.VideoReceive, AudioReceive: *capabilities.AudioReceive,
				AudioSend: *capabilities.AudioSend, DataChannels: *capabilities.DataChannels,
			}
		}
		var result media.StreamResult
		var err error
		if legacyLivPetStream {
			result, err = s.media.CreateExact(
				request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.ID, ownedBinding.DeviceSN,
				requested,
			)
		} else {
			result, err = s.media.Create(
				request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.ID, ownedBinding.DeviceSN,
				requested,
			)
		}
		if err != nil {
			writeMediaError(writer, err)
			return true
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Pragma", "no-cache")
		writeJSON(writer, http.StatusCreated, result)
		return true
	case "snapshots":
		if s.assets == nil {
			writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "media asset service is unavailable")
			return true
		}
		if request.Method == http.MethodGet {
			result, err := s.assets.GetSnapshot(request.Context(), ownedBinding.ID, ownedBinding.DeviceSN, parts[2])
			if err != nil {
				writeMediaError(writer, err)
				return true
			}
			writeJSON(writer, http.StatusOK, result)
			return true
		}
		var body struct {
			WaitACK *bool `json:"wait_ack,omitempty"`
		}
		if !decodeJSON(writer, request, &body) {
			return true
		}
		waitACK := true
		if body.WaitACK != nil {
			waitACK = *body.WaitACK
		}
		result, err := s.assets.CreateSnapshot(
			request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.ID, ownedBinding.DeviceSN, waitACK,
		)
		if err != nil {
			writeMediaError(writer, err)
			return true
		}
		writeJSON(writer, http.StatusOK, result)
		return true
	case "recordings":
		if s.assets == nil {
			writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "media asset service is unavailable")
			return true
		}
		if request.Method == http.MethodPost {
			var body struct {
				Duration int `json:"duration"`
			}
			if !decodeJSON(writer, request, &body) {
				return true
			}
			result, err := s.assets.CreateRecording(
				request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.ID, ownedBinding.DeviceSN, body.Duration,
			)
			if err != nil {
				writeMediaError(writer, err)
				return true
			}
			writeJSON(writer, http.StatusAccepted, result)
			return true
		}
		if len(parts) == 3 && parts[2] == "local" {
			offset, _ := strconv.Atoi(request.URL.Query().Get("offset"))
			limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
			result, err := s.assets.ListLocalRecordings(
				request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.DeviceSN,
				request.URL.Query().Get("date"), offset, limit,
			)
			if err != nil {
				writeMediaError(writer, err)
				return true
			}
			writeJSON(writer, http.StatusOK, result)
			return true
		}
		if len(parts) == 3 {
			result, err := s.assets.GetRecording(request.Context(), ownedBinding.ID, ownedBinding.DeviceSN, parts[2])
			if err != nil {
				writeMediaError(writer, err)
				return true
			}
			writeJSON(writer, http.StatusOK, result)
			return true
		}
		limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
		items, nextCursor, err := s.assets.ListRecordings(
			request.Context(), ownedBinding.ID, ownedBinding.DeviceSN, limit,
			request.URL.Query().Get("cursor"), request.URL.Query().Get("status"),
		)
		if err != nil {
			writeMediaError(writer, err)
			return true
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
		return true
	case "feed-audio":
		if s.assets == nil || s.device == nil {
			writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "media asset service is unavailable")
			return true
		}
		if request.Method == http.MethodDelete {
			result, err := s.device.SendCommands(request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.DeviceSN,
				[]devicemodule.CommandInput{{Code: "audio_reset", Value: json.RawMessage(`{}`)}}, true)
			if err != nil {
				writeDeviceError(writer, err)
				return true
			}
			writeJSON(writer, http.StatusOK, result)
			return true
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "feed audio multipart body is invalid")
			return true
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "feed audio file is required")
			return true
		}
		defer file.Close()
		payload, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(payload) > 1<<20 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "feed audio file is invalid")
			return true
		}
		result, err := s.assets.UploadFeedAudio(
			request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.DeviceSN,
			header.Header.Get("Content-Type"), payload,
		)
		if err != nil {
			writeMediaError(writer, err)
			return true
		}
		writeJSON(writer, http.StatusOK, result)
		return true
	}
	var body struct {
		Commands  []devicemodule.CommandInput `json:"commands"`
		Schedules []json.RawMessage           `json:"schedules,omitempty"`
		WaitACK   *bool                       `json:"wait_ack,omitempty"`
	}
	if !decodeJSON(writer, request, &body) {
		return true
	}
	waitACK := true
	if body.WaitACK != nil {
		waitACK = *body.WaitACK
	}
	commands := body.Commands
	if operation == "schedules" {
		if len(body.Schedules) > 16 {
			writeError(writer, http.StatusBadRequest, "schedules_too_many", "schedules array must not exceed 16 items")
			return true
		}
		schedules, err := json.Marshal(body.Schedules)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "schedules are invalid")
			return true
		}
		commands = []devicemodule.CommandInput{{Code: "set_schedule", Value: json.RawMessage(`{"schedules":` + string(schedules) + `}`)}}
	}
	for _, command := range commands {
		if reason := unsupportedLegacyWiFiReset(command); reason != "" {
			writeError(writer, http.StatusBadRequest, "invalid_request", reason)
			return true
		}
	}
	fullFactoryReset := false
	for _, command := range commands {
		if isFullFactoryReset(command) {
			fullFactoryReset = true
			break
		}
	}
	if fullFactoryReset {
		if operation != "commands" || len(commands) != 1 || !waitACK {
			writeError(writer, http.StatusBadRequest, "invalid_request", "factory_reset(all) must be a single command with wait_ack enabled")
			return true
		}
		reset, err := s.binding.Recommission(request.Context(), principal.InternalAppID, deviceToken, "all")
		if err != nil {
			if errors.Is(err, binding.ErrRecommissionRejected) {
				writeJSON(writer, http.StatusOK, factoryResetCommandResult(reset.Receipt))
				return true
			}
			if errors.Is(err, devicemodule.ErrDeviceOffline) || errors.Is(err, devicemodule.ErrDeviceBusy) ||
				errors.Is(err, devicemodule.ErrDeviceJammed) || errors.Is(err, devicemodule.ErrPublishFailed) {
				writeDeviceError(writer, err)
				return true
			}
			writeBindingError(writer, err)
			return true
		}
		writeJSON(writer, http.StatusOK, factoryResetCommandResult(reset.Receipt))
		return true
	}
	if s.device == nil {
		writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "device service is unavailable")
		return true
	}
	result, err := s.device.SendCommands(
		request.Context(), principal.InternalAppID, ownedBinding.DeviceID, ownedBinding.DeviceSN, commands, waitACK,
	)
	if err != nil {
		writeDeviceError(writer, err)
		return true
	}
	status := http.StatusOK
	if !waitACK {
		status = http.StatusAccepted
	}
	if operation == "schedules" {
		writeJSON(writer, status, map[string]any{"schedules": body.Schedules})
	} else {
		writeJSON(writer, status, result)
	}
	return true
}

func isFullFactoryReset(command devicemodule.CommandInput) bool {
	scope, ok := factoryResetScope(command)
	return ok && scope == "all"
}

func unsupportedLegacyWiFiReset(command devicemodule.CommandInput) string {
	if command.Code == "reset_wifi" {
		return "reset_wifi is unsupported; use recommission(runtime)"
	}
	if command.Code != "factory_reset" {
		return ""
	}
	scope, ok := factoryResetScope(command)
	if !ok {
		return "factory_reset requires an explicit scope; use recommission(runtime) to change Wi-Fi"
	}
	if scope == "wifi" {
		return "factory_reset(wifi) is unsupported; use recommission(runtime)"
	}
	return ""
}

func factoryResetScope(command devicemodule.CommandInput) (string, bool) {
	var value struct {
		Scope string `json:"scope"`
	}
	if json.Unmarshal(command.Value, &value) != nil || value.Scope == "" {
		return "", false
	}
	return value.Scope, true
}

func factoryResetCommandResult(receipt binding.ResetReceipt) devicemodule.SendCommandsResult {
	data := make(map[string]any, len(receipt.Data)+3)
	for key, value := range receipt.Data {
		data[key] = value
	}
	delete(data, "reset_scope")
	delete(data, "action")
	data["scope"] = "all"
	if receipt.Status == "success" {
		data["code"] = float64(0)
		data["will_reboot"] = true
	}
	return devicemodule.SendCommandsResult{Results: []devicemodule.CommandResult{{
		Index: 0, Code: "factory_reset", Status: receipt.Status, CommandID: receipt.CommandID,
		Data: data, Reason: receipt.Reason,
	}}}
}

func (s server) createActivation(writer http.ResponseWriter, request *http.Request) {
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return
	}
	var body struct {
		Method                string `json:"method"`
		BindingExternalUserID string `json:"binding_external_user_id"`
		IdempotencyKey        string `json:"idempotency_key"`
		PID                   string `json:"pid,omitempty"`
		SessionID             string `json:"session_id,omitempty"`
		ClientHelloCBOR       string `json:"client_hello_cbor,omitempty"`
		DeviceHelloCBOR       string `json:"device_hello_cbor,omitempty"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.PID != "" && body.PID != "feeder-x1" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "pid must be feeder-x1")
		return
	}
	created, err := s.activation.Create(request.Context(), activation.CreateInput{
		InternalAppID: principal.InternalAppID, BindingExternalUserID: body.BindingExternalUserID,
		IdempotencyKey: body.IdempotencyKey, Method: activation.Method(body.Method),
	})
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, activationResponse{Activation: created, IncludeCode: true})
}

func (s server) createActivationSession(writer http.ResponseWriter, request *http.Request, activationID string) {
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return
	}
	var body struct {
		SessionID             string `json:"session_id"`
		ClientHelloCBOR       string `json:"client_hello_cbor"`
		DeviceHelloCBOR       string `json:"device_hello_cbor"`
		BindingExternalUserID string `json:"binding_external_user_id"`
		IdempotencyKey        string `json:"idempotency_key"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.SessionID == "" || body.ClientHelloCBOR == "" || body.DeviceHelloCBOR == "" ||
		body.BindingExternalUserID == "" || body.IdempotencyKey == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "activation session request is invalid")
		return
	}
	value, err := s.activation.GetForInternalApp(request.Context(), principal.InternalAppID, activationID)
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	if value.Method == activation.MethodQR || value.BindingExternalUserID != body.BindingExternalUserID {
		writeError(writer, http.StatusBadRequest, "invalid_request", "activation session request is invalid")
		return
	}
	writeJSON(writer, http.StatusOK, activationResponse{Activation: value})
}

func (s server) getActivation(writer http.ResponseWriter, request *http.Request, activationID string) {
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return
	}
	value, err := s.activation.GetForInternalApp(request.Context(), principal.InternalAppID, activationID)
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, activationResponse{Activation: value})
}

func (s server) listDevices(writer http.ResponseWriter, request *http.Request) {
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return
	}
	var body struct {
		BindingExternalUserID string   `json:"binding_external_user_id"`
		DeviceSNs             []string `json:"device_sns,omitempty"`
		Limit                 int      `json:"limit,omitempty"`
		Offset                int      `json:"offset,omitempty"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	devices, err := s.activation.ListDevicesForBinding(request.Context(), principal.InternalAppID, body.BindingExternalUserID)
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	requested := make(map[string]struct{}, len(body.DeviceSNs))
	for _, deviceSN := range body.DeviceSNs {
		requested[deviceSN] = struct{}{}
	}
	if body.Limit <= 0 || body.Limit > 100 {
		body.Limit = 100
	}
	if body.Offset < 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request", "offset must not be negative")
		return
	}
	response := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		if len(requested) > 0 {
			if _, ok := requested[device.ID]; !ok {
				continue
			}
		}
		lastSeenAt := ""
		if device.LastSeenAt != nil {
			lastSeenAt = device.LastSeenAt.UTC().Format(time.RFC3339)
		}
		response = append(response, map[string]any{
			"device_sn": device.ID, "pid": "feeder-x1", "status": device.Status,
			"nickname": "", "is_subscription": false,
			"activation_id": device.ActivationID, "aws_iot_endpoint": device.Endpoint,
			"binding_external_user_id": device.BindingExternalUserID,
			"online":                   device.Online, "last_seen_at": lastSeenAt,
		})
	}
	if body.Offset >= len(response) {
		response = response[:0]
	} else {
		response = response[body.Offset:]
	}
	if len(response) > body.Limit {
		response = response[:body.Limit]
	}
	writeJSON(writer, http.StatusOK, map[string]any{"devices": response})
}

func (s server) issueDeviceToken(writer http.ResponseWriter, request *http.Request) {
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return
	}
	if s.binding == nil {
		writeError(writer, http.StatusServiceUnavailable, "binding_unavailable", "binding service is unavailable")
		return
	}
	var body struct {
		DeviceSN string `json:"device_sn"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	issued, err := s.binding.IssueToken(request.Context(), principal.InternalAppID, body.DeviceSN)
	if err != nil {
		writeBindingError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, issued)
}

func (s server) unbindDevice(writer http.ResponseWriter, request *http.Request, token string) {
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return
	}
	if s.binding == nil {
		writeError(writer, http.StatusServiceUnavailable, "binding_unavailable", "binding service is unavailable")
		return
	}
	released, err := s.binding.Unbind(request.Context(), principal.InternalAppID, token)
	if err != nil {
		writeBindingError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"device_sn": released.DeviceSN, "unbound": true})
}

func (s server) recommissionDevice(writer http.ResponseWriter, request *http.Request, token string) {
	principal, ok := InternalAppPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "principal_missing", "internal app principal is unavailable")
		return
	}
	if s.binding == nil {
		writeError(writer, http.StatusServiceUnavailable, "binding_unavailable", "binding service is unavailable")
		return
	}
	var body struct {
		ResetScope string `json:"reset_scope"`
	}
	if !decodeOptionalJSON(writer, request, &body) {
		return
	}
	if body.ResetScope == "" {
		body.ResetScope = "runtime"
	}
	result, err := s.binding.Recommission(request.Context(), principal.InternalAppID, token, body.ResetScope)
	if err != nil {
		writeBindingError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"device_sn": result.Binding.DeviceSN, "unbound": true, "recommissioning": true, "reset_scope": body.ResetScope,
	})
}

func (s server) prepareDevice(writer http.ResponseWriter, request *http.Request, expectedMethod activation.Method) {
	var body struct {
		ActivationID string `json:"activation_id"`
		Code         string `json:"activation_code"`
		CSRPEM       string `json:"csr_pem"`
		Method       string `json:"method,omitempty"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if expectedMethod == "" {
		expectedMethod = activation.Method(body.Method)
		if expectedMethod != activation.MethodAP && expectedMethod != activation.MethodBLE {
			writeError(writer, http.StatusBadRequest, "invalid_request", "session method must be ap or ble")
			return
		}
	}
	installation, err := s.activation.Prepare(request.Context(), activation.PrepareInput{
		ActivationID: body.ActivationID, Code: body.Code, CSRPEM: body.CSRPEM, ExpectedMethod: expectedMethod,
	})
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{
		"activation_id": installation.ActivationID, "device_sn": installation.DeviceID,
		"device_id": installation.DeviceID, "thing_name": installation.DeviceID,
		"certificate_id":  installation.CertificateID,
		"certificate_pem": installation.Certificate, "aws_iot_endpoint": installation.Endpoint,
		"installation_nonce": installation.ProofNonce, "pid": "feeder-x1",
		"aws_region":                  s.deviceRuntime.AWSRegion,
		"aws_iot_credential_endpoint": s.deviceRuntime.CredentialEndpoint,
		"aws_iot_role_alias":          s.deviceRuntime.RoleAlias,
		"topic_prefix":                s.deviceRuntime.TopicPrefix,
		"aws_iot_shadow_name":         s.deviceRuntime.ShadowName,
		"aws_kvs_channel_prefix":      s.deviceRuntime.KVSChannelPrefix,
	})
}

func (s server) confirmDevice(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ActivationID string `json:"activation_id"`
		Code         string `json:"activation_code"`
		Proof        string `json:"installation_proof"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	confirmed, err := s.activation.Confirm(request.Context(), activation.ConfirmInput{
		ActivationID: body.ActivationID, Code: body.Code, Proof: body.Proof,
	})
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, activationResponse{Activation: confirmed})
}

type activationResponse struct {
	Activation  activation.Activation
	IncludeCode bool
}

func (response activationResponse) MarshalJSON() ([]byte, error) {
	status := "pending"
	activated := false
	switch response.Activation.Status {
	case activation.StatusInstalling:
		status = "installing"
	case activation.StatusActivated:
		status = "activated"
		activated = true
	}
	type payload struct {
		ActivationID        string    `json:"activation_id"`
		Method              string    `json:"method"`
		Status              string    `json:"status"`
		ExpiresAt           time.Time `json:"expires_at"`
		Activated           bool      `json:"activated"`
		DeviceSN            string    `json:"device_sn"`
		PID                 string    `json:"pid"`
		ActivationCode      string    `json:"activation_code,omitempty"`
		QRAuthorizationCode string    `json:"qr_authorization_code,omitempty"`
	}
	value := payload{
		ActivationID: response.Activation.ID, Method: string(response.Activation.Method),
		Status: status, ExpiresAt: response.Activation.ExpiresAt, Activated: activated,
		DeviceSN: response.Activation.DeviceSN, PID: "feeder-x1",
	}
	if response.IncludeCode {
		value.ActivationCode = response.Activation.Code
		if response.Activation.Method == activation.MethodQR {
			value.QRAuthorizationCode = response.Activation.Code
		}
	}
	return json.Marshal(value)
}

func decodeOptionalJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	if request.Body == nil || request.ContentLength == 0 {
		return true
	}
	return decodeJSON(writer, request, target)
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	return true
}

func writeBindingError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, binding.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	case errors.Is(err, binding.ErrBindingNotFound):
		writeError(writer, http.StatusBadRequest, "binding_not_found", "binding was not found")
	case errors.Is(err, binding.ErrTokenNotFound):
		writeError(writer, http.StatusBadRequest, "token_not_found", "device token was not found")
	case errors.Is(err, binding.ErrForbidden):
		writeError(writer, http.StatusForbidden, "device_operation_forbidden", "device operation is forbidden")
	case errors.Is(err, binding.ErrRecommissionUnavailable), errors.Is(err, binding.ErrRecommissionRejected):
		writeError(writer, http.StatusConflict, "device_recommission_unavailable", "device did not acknowledge recommissioning; binding remains active")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func writeDeviceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, devicemodule.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	case errors.Is(err, devicemodule.ErrDeviceOffline):
		writeError(writer, http.StatusConflict, "device_offline", "device is offline")
	case errors.Is(err, devicemodule.ErrDeviceBusy):
		writeError(writer, http.StatusConflict, "device_busy", "device is busy")
	case errors.Is(err, devicemodule.ErrDeviceJammed):
		writeError(writer, http.StatusConflict, "device_jammed", "device is jammed")
	case errors.Is(err, devicemodule.ErrPublishFailed):
		writeError(writer, http.StatusServiceUnavailable, "device_service_unavailable", "device command could not be published")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func writeMediaError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, media.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "media request is invalid")
	case errors.Is(err, media.ErrInvalidCapabilities):
		writeError(writer, http.StatusBadRequest, "invalid_stream_capabilities", "invalid requested stream capabilities")
	case errors.Is(err, media.ErrStreamCapabilityUnavailable):
		writeError(writer, http.StatusConflict, "stream_capability_unavailable", "device could not grant the required stream capabilities")
	case errors.Is(err, media.ErrStreamRejected):
		writeError(writer, http.StatusConflict, "stream_rejected", "device rejected stream start")
	case errors.Is(err, media.ErrStreamNotFound):
		writeError(writer, http.StatusNotFound, "stream_not_found", "stream was not found")
	case errors.Is(err, media.ErrSnapshotNotFound):
		writeError(writer, http.StatusNotFound, "snapshot_not_found", "snapshot was not found")
	case errors.Is(err, media.ErrRecordingNotFound):
		writeError(writer, http.StatusNotFound, "recording_not_found", "recording was not found")
	case errors.Is(err, media.ErrRecordingRejected):
		writeError(writer, http.StatusConflict, "recording_rejected", "device rejected recording start")
	case errors.Is(err, devicemodule.ErrDeviceOffline):
		writeError(writer, http.StatusConflict, "device_offline", "device is offline")
	default:
		writeError(writer, http.StatusServiceUnavailable, "kvs_unavailable", "WebRTC playback is unavailable")
	}
}

func writeActivationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, activation.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	case errors.Is(err, activation.ErrActivationNotFound):
		writeError(writer, http.StatusNotFound, "activation_not_found", "activation was not found")
	case errors.Is(err, activation.ErrActivationUnauthorized), errors.Is(err, activation.ErrInstallationProofInvalid):
		writeError(writer, http.StatusForbidden, "activation_forbidden", "activation authorization was rejected")
	case errors.Is(err, activation.ErrActivationExpired):
		writeError(writer, http.StatusGone, "activation_expired", "activation has expired")
	case errors.Is(err, activation.ErrActivationInProgress), errors.Is(err, activation.ErrPreparationInProgress):
		writeError(writer, http.StatusConflict, "activation_in_progress", "activation operation is already in progress")
	case errors.Is(err, activation.ErrIdempotencyConflict):
		writeError(writer, http.StatusConflict, "idempotency_conflict", "idempotency key was used for another request")
	case errors.Is(err, activation.ErrDeviceBindingConflict):
		writeError(writer, http.StatusConflict, "device_binding_conflict", "device already has an active binding")
	default:
		writeError(writer, http.StatusInternalServerError, "activation_failed", "activation operation failed")
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"error_code": code, "message": message, "error": message})
}
