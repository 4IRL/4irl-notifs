package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
)

// maxExchangeBodyBytes caps an exchange request body. The body is a JSON object holding an
// app id and one GitHub OIDC token (a few KB), so 32 KiB is generous.
const maxExchangeBodyBytes = 32 << 10

// Error messages returned in the {"error": "..."} body of app identity responses.
const (
	errMsgInvalidIdentityToken   = "invalid identity token"
	errMsgBindingMismatch        = "identity does not match the approved binding"
	errMsgTooManyPendingRequests = "too many pending identity requests"
	errMsgIdentityUnavailable    = "identity unavailable"
	errMsgInvalidRequestID       = "invalid request id"
	errMsgInvalidKeyID           = "invalid key id"
	errMsgRequestNotFound        = "request does not exist"
	errMsgRequestNotPending      = "request is not pending"
	errMsgKeyNotFound            = "key does not exist"
	errMsgAppAlreadyBound        = "app already bound"
	errMsgWorkflowAlreadyBound   = "workflow identity already bound"
	errMsgAppIDOverlap           = "app id overlaps an approved app id"
)

// AppIdentity is the app identity surface the exchange, the /v1/admin/app-identity endpoints and
// the requireAppKey middleware call. *appidentity.Service satisfies it.
type AppIdentity interface {
	// ResolveKey authenticates a presented app key. An error wrapping appidentity.ErrInvalidKey
	// means the key is not usable; any other error is a lookup failure.
	ResolveKey(ctx context.Context, key string) (appidentity.ResolvedKey, error)
	Exchange(ctx context.Context, appID string, token string) (appidentity.ExchangeResult, error)
	ListRequests(ctx context.Context) ([]appidentity.Request, error)
	ApproveRequest(ctx context.Context, requestID int64) (appidentity.Binding, error)
	RejectRequest(ctx context.Context, requestID int64) (appidentity.Request, error)
	ListBindings(ctx context.Context) ([]appidentity.BindingWithKeys, error)
	RevokeKey(ctx context.Context, keyID int64) (appidentity.Key, error)
	Unbind(ctx context.Context, appID string) (bool, error)
}

// exchangeRequestBody is the JSON body of POST /v1/app-identity/exchange.
type exchangeRequestBody struct {
	AppID string `json:"app_id"`
	Token string `json:"token"`
}

// exchangePendingResponseBody is the 202 body: the request awaits the operator.
type exchangePendingResponseBody struct {
	Status    string `json:"status"`
	RequestID int64  `json:"request_id"`
}

// exchangeIssuedResponseBody is the 200 body. It is the only place a plaintext key appears.
type exchangeIssuedResponseBody struct {
	Status string `json:"status"`
	AppID  string `json:"app_id"`
	Key    string `json:"key"`
	KeyID  int64  `json:"key_id"`
}

// identityRequestBody is the JSON form of one registration request.
type identityRequestBody struct {
	ID                 int64   `json:"id"`
	AppID              string  `json:"app_id"`
	GitHubRepository   string  `json:"github_repository"`
	GitHubRepositoryID int64   `json:"github_repository_id"`
	WorkflowPath       string  `json:"workflow_path"`
	GitRef             string  `json:"git_ref"`
	Environment        *string `json:"environment"`
	GitHubRunID        string  `json:"github_run_id"`
	Status             string  `json:"status"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

// identityKeyBody is the JSON form of one app key. It never carries the key itself.
type identityKeyBody struct {
	ID            int64   `json:"id"`
	AppID         string  `json:"app_id"`
	KeyHint       string  `json:"key_hint"`
	State         string  `json:"state"`
	CreatedAt     string  `json:"created_at"`
	SupersededAt  *string `json:"superseded_at"`
	RevokedAt     *string `json:"revoked_at"`
	RevokedReason *string `json:"revoked_reason"`
	LastUsedAt    *string `json:"last_used_at"`
}

// identityBindingBody is the JSON form of one binding; keys is never null.
type identityBindingBody struct {
	AppID              string            `json:"app_id"`
	GitHubRepository   string            `json:"github_repository"`
	GitHubRepositoryID int64             `json:"github_repository_id"`
	GitHubOwnerID      int64             `json:"github_owner_id"`
	WorkflowPath       string            `json:"workflow_path"`
	GitRef             string            `json:"git_ref"`
	Environment        *string           `json:"environment"`
	ApprovedAt         string            `json:"approved_at"`
	Keys               []identityKeyBody `json:"keys"`
}

type identityRequestsResponseBody struct {
	Requests []identityRequestBody `json:"requests"`
}

type identityRequestResponseBody struct {
	Request identityRequestBody `json:"request"`
}

type identityBindingResponseBody struct {
	Binding identityBindingBody `json:"binding"`
}

type identityBindingsResponseBody struct {
	Bindings []identityBindingBody `json:"bindings"`
}

type identityKeyResponseBody struct {
	Key identityKeyBody `json:"key"`
}

type unbindResponseBody struct {
	AppID   string `json:"app_id"`
	Deleted bool   `json:"deleted"`
}

// requireAppIdentity wraps an identity handler so that it fails closed: with no AppIdentity
// configured the request is answered 503 before anything else is looked at. Every identity
// response is marked uncacheable: the exchange body carries a plaintext key.
func (server *Server) requireAppIdentity(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.appIdentity == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgIdentityUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handleExchange serves POST /v1/app-identity/exchange: 202 when the request awaits the
// operator, 200 with the app's key once it is approved.
func (server *Server) handleExchange(responseWriter http.ResponseWriter, request *http.Request) {
	var body exchangeRequestBody
	if !decodeJSON(responseWriter, request, &body, maxExchangeBodyBytes) {
		return
	}
	result, exchangeErr := server.appIdentity.Exchange(request.Context(), body.AppID, body.Token)
	if exchangeErr != nil {
		server.writeIdentityError(responseWriter, request, exchangeErr)
		return
	}
	switch result.Status {
	case appidentity.ExchangeIssued:
		writeJSON(responseWriter, http.StatusOK, exchangeIssuedResponseBody{
			Status: result.Status, AppID: result.AppID, Key: result.Key, KeyID: result.KeyID,
		})
	case appidentity.ExchangePending:
		writeJSON(responseWriter, http.StatusAccepted, exchangePendingResponseBody{
			Status: result.Status, RequestID: result.RequestID,
		})
	default:
		// Fail closed: a status this handler does not know is never answered as a success.
		server.logger.Error("app identity exchange returned an unknown status", "status", result.Status, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	}
}

// handleListIdentityRequests serves GET /v1/admin/app-identity/requests.
func (server *Server) handleListIdentityRequests(responseWriter http.ResponseWriter, request *http.Request) {
	requests, listErr := server.appIdentity.ListRequests(request.Context())
	if listErr != nil {
		server.writeIdentityError(responseWriter, request, listErr)
		return
	}
	bodies := make([]identityRequestBody, 0, len(requests))
	for _, listed := range requests {
		bodies = append(bodies, newIdentityRequestBody(listed))
	}
	writeJSON(responseWriter, http.StatusOK, identityRequestsResponseBody{Requests: bodies})
}

// handleApproveIdentityRequest serves POST /v1/admin/app-identity/requests/{id}/approve (201).
func (server *Server) handleApproveIdentityRequest(responseWriter http.ResponseWriter, request *http.Request) {
	requestID, ok := parsePositiveID(responseWriter, request, errMsgInvalidRequestID)
	if !ok {
		return
	}
	binding, approveErr := server.appIdentity.ApproveRequest(request.Context(), requestID)
	if approveErr != nil {
		server.writeIdentityError(responseWriter, request, approveErr)
		return
	}
	writeJSON(responseWriter, http.StatusCreated,
		identityBindingResponseBody{Binding: newIdentityBindingBody(binding, nil)})
}

// handleRejectIdentityRequest serves POST /v1/admin/app-identity/requests/{id}/reject (200).
func (server *Server) handleRejectIdentityRequest(responseWriter http.ResponseWriter, request *http.Request) {
	requestID, ok := parsePositiveID(responseWriter, request, errMsgInvalidRequestID)
	if !ok {
		return
	}
	rejected, rejectErr := server.appIdentity.RejectRequest(request.Context(), requestID)
	if rejectErr != nil {
		server.writeIdentityError(responseWriter, request, rejectErr)
		return
	}
	writeJSON(responseWriter, http.StatusOK, identityRequestResponseBody{Request: newIdentityRequestBody(rejected)})
}

// handleListIdentityBindings serves GET /v1/admin/app-identity/bindings.
func (server *Server) handleListIdentityBindings(responseWriter http.ResponseWriter, request *http.Request) {
	bindings, listErr := server.appIdentity.ListBindings(request.Context())
	if listErr != nil {
		server.writeIdentityError(responseWriter, request, listErr)
		return
	}
	bodies := make([]identityBindingBody, 0, len(bindings))
	for _, listed := range bindings {
		bodies = append(bodies, newIdentityBindingBody(listed.Binding, listed.Keys))
	}
	writeJSON(responseWriter, http.StatusOK, identityBindingsResponseBody{Bindings: bodies})
}

// handleRevokeIdentityKey serves POST /v1/admin/app-identity/keys/{id}/revoke (200), idempotently.
func (server *Server) handleRevokeIdentityKey(responseWriter http.ResponseWriter, request *http.Request) {
	keyID, ok := parsePositiveID(responseWriter, request, errMsgInvalidKeyID)
	if !ok {
		return
	}
	key, revokeErr := server.appIdentity.RevokeKey(request.Context(), keyID)
	if revokeErr != nil {
		server.writeIdentityError(responseWriter, request, revokeErr)
		return
	}
	writeJSON(responseWriter, http.StatusOK, identityKeyResponseBody{Key: newIdentityKeyBody(key)})
}

// handleUnbindApp serves DELETE /v1/admin/app-identity/bindings/{app_id} (200), idempotently:
// an app with no binding answers deleted=false.
func (server *Server) handleUnbindApp(responseWriter http.ResponseWriter, request *http.Request) {
	appID := request.PathValue(pathValueAppID)
	deleted, unbindErr := server.appIdentity.Unbind(request.Context(), appID)
	if unbindErr != nil {
		server.writeIdentityError(responseWriter, request, unbindErr)
		return
	}
	writeJSON(responseWriter, http.StatusOK, unbindResponseBody{AppID: appID, Deleted: deleted})
}

func newIdentityRequestBody(request appidentity.Request) identityRequestBody {
	return identityRequestBody{
		ID:                 request.ID,
		AppID:              request.AppID,
		GitHubRepository:   request.Repository,
		GitHubRepositoryID: request.RepositoryID,
		WorkflowPath:       request.WorkflowPath,
		GitRef:             request.Ref,
		Environment:        optionalText(request.Environment),
		GitHubRunID:        request.GitHubRunID,
		Status:             request.Status,
		CreatedAt:          request.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          request.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func newIdentityBindingBody(binding appidentity.Binding, keys []appidentity.Key) identityBindingBody {
	keyBodies := make([]identityKeyBody, 0, len(keys))
	for _, key := range keys {
		keyBodies = append(keyBodies, newIdentityKeyBody(key))
	}
	return identityBindingBody{
		AppID:              binding.AppID,
		GitHubRepository:   binding.Repository,
		GitHubRepositoryID: binding.RepositoryID,
		GitHubOwnerID:      binding.RepositoryOwnerID,
		WorkflowPath:       binding.WorkflowPath,
		GitRef:             binding.Ref,
		Environment:        optionalText(binding.Environment),
		ApprovedAt:         binding.ApprovedAt.UTC().Format(time.RFC3339),
		Keys:               keyBodies,
	}
}

func newIdentityKeyBody(key appidentity.Key) identityKeyBody {
	return identityKeyBody{
		ID:            key.ID,
		AppID:         key.AppID,
		KeyHint:       key.Hint,
		State:         string(key.State),
		CreatedAt:     key.CreatedAt.UTC().Format(time.RFC3339),
		SupersededAt:  formatUTC(key.SupersededAt),
		RevokedAt:     formatUTC(key.RevokedAt),
		RevokedReason: optionalText(key.RevokedReason),
		LastUsedAt:    formatUTC(key.LastUsedAt),
	}
}

// optionalText maps the empty string to JSON null.
func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// writeIdentityError is the single chokepoint mapping an app identity error to an HTTP
// response. Validation errors carry a client-safe message; every token check failure is the
// same fixed 401 (the specific reason is logged by the service, never returned); known
// sentinels map to fixed messages; anything else is logged (path only, never the request
// body, which holds a token) and answered with a generic 500.
func (server *Server) writeIdentityError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	var validationErr *appidentity.ValidationError
	switch {
	case errors.As(err, &validationErr):
		writeError(responseWriter, http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, appidentity.ErrInvalidToken):
		writeError(responseWriter, http.StatusUnauthorized, errMsgInvalidIdentityToken)
	case errors.Is(err, appidentity.ErrKeysUnavailable):
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgIdentityUnavailable)
	case errors.Is(err, appidentity.ErrBindingMismatch):
		writeError(responseWriter, http.StatusForbidden, errMsgBindingMismatch)
	case errors.Is(err, appidentity.ErrPendingCap):
		writeError(responseWriter, http.StatusTooManyRequests, errMsgTooManyPendingRequests)
	case errors.Is(err, appidentity.ErrRequestNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgRequestNotFound)
	case errors.Is(err, appidentity.ErrKeyNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgKeyNotFound)
	case errors.Is(err, appidentity.ErrRequestNotPending):
		writeError(responseWriter, http.StatusConflict, errMsgRequestNotPending)
	case errors.Is(err, appidentity.ErrAppAlreadyBound):
		writeError(responseWriter, http.StatusConflict, errMsgAppAlreadyBound)
	case errors.Is(err, appidentity.ErrWorkflowIdentityBound):
		writeError(responseWriter, http.StatusConflict, errMsgWorkflowAlreadyBound)
	case errors.Is(err, appidentity.ErrAppIDOverlap):
		writeError(responseWriter, http.StatusConflict, errMsgAppIDOverlap)
	case errors.Is(err, context.Canceled):
		// The client went away: not a server fault, and a client that can disconnect at will
		// must not be able to flood the error log. The response is moot, but stays the generic 500.
		server.logger.Debug("app identity request canceled", "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	case errors.Is(err, context.DeadlineExceeded):
		// A deadline is ambiguous (a client-supplied one or a server/driver one such as a
		// saturated pool), so it is logged where an operator can see it.
		server.logger.Warn("app identity request deadline exceeded", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	default:
		server.logger.Error("app identity service error", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	}
}
