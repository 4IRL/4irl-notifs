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
	errMsgRequestChanged         = "registration request changed during approval"
	errMsgAppRegistryUnavailable = "app registry unavailable"
	errMsgAppIDReserved          = "app id is reserved"

	errMsgInvalidName            = "invalid name"
	errMsgInvalidPrefix          = "invalid prefix"
	errMsgExchangeTarget         = "send exactly one of app_id or name"
	errMsgRegistrarOwnership     = "app id is not owned by this registrar"
	errMsgBindingNotFound        = "binding does not exist"
	errMsgRegistrarAppLimit      = "registrar app limit reached"
	errMsgPrefixTaken            = "prefix already used"
	errMsgPrefixOverlap          = "prefix overlaps an approved app id or registrar"
	errMsgRequestIsRegistrar     = "request is a registrar request"
	errMsgRequestNotRegistrar    = "request is not a registrar request"
	errMsgAppIDInRegistrarPrefix = "app id is reserved by a registrar"
	errMsgRegistrationChanged    = "registration changed, retry"
	errMsgInvalidRegistrarID     = "invalid registrar id"
)

// AppIdentity is the app identity surface the exchange, the /v1/admin/app-identity endpoints and
// the requireAppKey middleware call. *appidentity.Service satisfies it.
type AppIdentity interface {
	// ResolveKey authenticates a presented app key. An error wrapping appidentity.ErrInvalidKey
	// means the key is not usable; any other error is a lookup failure.
	ResolveKey(ctx context.Context, key string) (appidentity.ResolvedKey, error)
	Exchange(ctx context.Context, input appidentity.ExchangeInput) (appidentity.ExchangeResult, error)
	ListRequests(ctx context.Context) ([]appidentity.Request, error)
	ApproveRequest(ctx context.Context, requestID int64) (appidentity.Binding, error)
	RejectRequest(ctx context.Context, requestID int64) (appidentity.Request, error)
	ListBindings(ctx context.Context) ([]appidentity.BindingWithKeys, error)
	RevokeKey(ctx context.Context, keyID int64) (appidentity.Key, error)
	Unbind(ctx context.Context, appID string) (bool, error)
	ApproveRegistrarRequest(ctx context.Context, requestID int64, prefix string) (appidentity.WorkflowRegistrar, error)
	ListRegistrars(ctx context.Context) ([]appidentity.WorkflowRegistrar, error)
	DeleteRegistrar(ctx context.Context, registrarID int64) (bool, int, error)
	AcknowledgeBinding(ctx context.Context, appID string) (appidentity.BindingWithKeys, error)
	AcknowledgeAllBindings(ctx context.Context, appIDs []string) (int, error)
}

// exchangeRequestBody is the JSON body of POST /v1/app-identity/exchange. Exactly one of
// app_id (normal app) or name (registrar) is sent; the service enforces that.
type exchangeRequestBody struct {
	AppID string `json:"app_id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

type approveRegistrarRequestBody struct {
	Prefix string `json:"prefix"`
}

type acknowledgeAllRequestBody struct {
	AppIDs []string `json:"app_ids"`
}

// identityRegistrarBody is the JSON form of one registrar.
type identityRegistrarBody struct {
	ID                 int64   `json:"id"`
	Prefix             string  `json:"prefix"`
	GitHubRepository   string  `json:"github_repository"`
	GitHubRepositoryID int64   `json:"github_repository_id"`
	GitHubOwnerID      int64   `json:"github_owner_id"`
	WorkflowPath       string  `json:"workflow_path"`
	GitRef             string  `json:"git_ref"`
	Environment        *string `json:"environment"`
	ApprovedAt         string  `json:"approved_at"`
	AppCount           int     `json:"app_count"`
	AppLimit           int     `json:"app_limit"`
}

// identityBindingRegistrarBody is the registrar reference inside a binding.
type identityBindingRegistrarBody struct {
	ID     int64  `json:"id"`
	Prefix string `json:"prefix"`
}

type identityRegistrarResponseBody struct {
	Registrar identityRegistrarBody `json:"registrar"`
}

type identityRegistrarsResponseBody struct {
	Registrars []identityRegistrarBody `json:"registrars"`
}

type deleteRegistrarResponseBody struct {
	ID          int64 `json:"id"`
	Deleted     bool  `json:"deleted"`
	UnboundApps int   `json:"unbound_apps"`
}

type acknowledgeAllResponseBody struct {
	Acknowledged int `json:"acknowledged"`
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
	Kind               string  `json:"kind"`
	AppID              *string `json:"app_id"`
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
	AppID              string                        `json:"app_id"`
	GitHubRepository   string                        `json:"github_repository"`
	GitHubRepositoryID int64                         `json:"github_repository_id"`
	GitHubOwnerID      int64                         `json:"github_owner_id"`
	WorkflowPath       string                        `json:"workflow_path"`
	GitRef             string                        `json:"git_ref"`
	Environment        *string                       `json:"environment"`
	ApprovedAt         string                        `json:"approved_at"`
	Registrar          *identityBindingRegistrarBody `json:"registrar"`
	AcknowledgedAt     *string                       `json:"acknowledged_at"`
	Keys               []identityKeyBody             `json:"keys"`
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
	result, exchangeErr := server.appIdentity.Exchange(request.Context(),
		appidentity.ExchangeInput{AppID: body.AppID, Name: body.Name, Token: body.Token})
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

// handleApproveRegistrarRequest serves POST /v1/admin/app-identity/requests/{id}/approve-registrar (201).
func (server *Server) handleApproveRegistrarRequest(responseWriter http.ResponseWriter, request *http.Request) {
	requestID, ok := parsePositiveID(responseWriter, request, errMsgInvalidRequestID)
	if !ok {
		return
	}
	var body approveRegistrarRequestBody
	if !decodeJSON(responseWriter, request, &body, maxExchangeBodyBytes) {
		return
	}
	registrar, approveErr := server.appIdentity.ApproveRegistrarRequest(request.Context(), requestID, body.Prefix)
	if approveErr != nil {
		server.writeIdentityError(responseWriter, request, approveErr)
		return
	}
	writeJSON(responseWriter, http.StatusCreated, identityRegistrarResponseBody{Registrar: newIdentityRegistrarBody(registrar)})
}

// handleListRegistrars serves GET /v1/admin/app-identity/registrars.
func (server *Server) handleListRegistrars(responseWriter http.ResponseWriter, request *http.Request) {
	registrars, listErr := server.appIdentity.ListRegistrars(request.Context())
	if listErr != nil {
		server.writeIdentityError(responseWriter, request, listErr)
		return
	}
	bodies := make([]identityRegistrarBody, 0, len(registrars))
	for _, listed := range registrars {
		bodies = append(bodies, newIdentityRegistrarBody(listed))
	}
	writeJSON(responseWriter, http.StatusOK, identityRegistrarsResponseBody{Registrars: bodies})
}

// handleDeleteRegistrar serves DELETE /v1/admin/app-identity/registrars/{id} (200), idempotently.
func (server *Server) handleDeleteRegistrar(responseWriter http.ResponseWriter, request *http.Request) {
	registrarID, ok := parsePositiveID(responseWriter, request, errMsgInvalidRegistrarID)
	if !ok {
		return
	}
	deleted, unbound, deleteErr := server.appIdentity.DeleteRegistrar(request.Context(), registrarID)
	if deleteErr != nil {
		server.writeIdentityError(responseWriter, request, deleteErr)
		return
	}
	writeJSON(responseWriter, http.StatusOK,
		deleteRegistrarResponseBody{ID: registrarID, Deleted: deleted, UnboundApps: unbound})
}

// handleAcknowledgeBinding serves POST /v1/admin/app-identity/bindings/{app_id}/acknowledge (200).
func (server *Server) handleAcknowledgeBinding(responseWriter http.ResponseWriter, request *http.Request) {
	listed, acknowledgeErr := server.appIdentity.AcknowledgeBinding(request.Context(), request.PathValue(pathValueAppID))
	if acknowledgeErr != nil {
		server.writeIdentityError(responseWriter, request, acknowledgeErr)
		return
	}
	writeJSON(responseWriter, http.StatusOK,
		identityBindingResponseBody{Binding: newIdentityBindingBody(listed.Binding, listed.Keys)})
}

// handleAcknowledgeAllBindings serves POST /v1/admin/app-identity/bindings/acknowledge-all (200).
func (server *Server) handleAcknowledgeAllBindings(responseWriter http.ResponseWriter, request *http.Request) {
	var body acknowledgeAllRequestBody
	if !decodeJSON(responseWriter, request, &body, maxExchangeBodyBytes) {
		return
	}
	changed, acknowledgeErr := server.appIdentity.AcknowledgeAllBindings(request.Context(), body.AppIDs)
	if acknowledgeErr != nil {
		server.writeIdentityError(responseWriter, request, acknowledgeErr)
		return
	}
	writeJSON(responseWriter, http.StatusOK, acknowledgeAllResponseBody{Acknowledged: changed})
}

func newIdentityRegistrarBody(registrar appidentity.WorkflowRegistrar) identityRegistrarBody {
	return identityRegistrarBody{
		ID:                 registrar.ID,
		Prefix:             registrar.Prefix,
		GitHubRepository:   registrar.Repository,
		GitHubRepositoryID: registrar.RepositoryID,
		GitHubOwnerID:      registrar.RepositoryOwnerID,
		WorkflowPath:       registrar.WorkflowPath,
		GitRef:             registrar.Ref,
		Environment:        optionalText(registrar.Environment),
		ApprovedAt:         registrar.ApprovedAt.UTC().Format(time.RFC3339),
		AppCount:           registrar.AppCount,
		AppLimit:           registrar.AppLimit,
	}
}

func newIdentityRequestBody(request appidentity.Request) identityRequestBody {
	return identityRequestBody{
		ID:                 request.ID,
		Kind:               request.Kind,
		AppID:              optionalText(request.AppID),
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
	var registrar *identityBindingRegistrarBody
	if binding.RegistrarID != nil {
		registrar = &identityBindingRegistrarBody{ID: *binding.RegistrarID, Prefix: binding.RegistrarPrefix}
	}
	return identityBindingBody{
		Registrar:          registrar,
		AcknowledgedAt:     formatUTC(binding.AcknowledgedAt),
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
	case errors.Is(err, appidentity.ErrInvalidName):
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidName)
	case errors.Is(err, appidentity.ErrInvalidPrefix):
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidPrefix)
	case errors.Is(err, appidentity.ErrExchangeTarget):
		writeError(responseWriter, http.StatusBadRequest, errMsgExchangeTarget)
	case errors.Is(err, appidentity.ErrBindingMismatch):
		writeError(responseWriter, http.StatusForbidden, errMsgBindingMismatch)
	case errors.Is(err, appidentity.ErrRegistrarOwnership):
		writeError(responseWriter, http.StatusForbidden, errMsgRegistrarOwnership)
	case errors.Is(err, appidentity.ErrBindingNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgBindingNotFound)
	case errors.Is(err, appidentity.ErrRegistrarAppLimit):
		writeError(responseWriter, http.StatusConflict, errMsgRegistrarAppLimit)
	case errors.Is(err, appidentity.ErrPrefixTaken):
		writeError(responseWriter, http.StatusConflict, errMsgPrefixTaken)
	case errors.Is(err, appidentity.ErrPrefixOverlap):
		writeError(responseWriter, http.StatusConflict, errMsgPrefixOverlap)
	case errors.Is(err, appidentity.ErrRequestIsRegistrar):
		writeError(responseWriter, http.StatusConflict, errMsgRequestIsRegistrar)
	case errors.Is(err, appidentity.ErrRequestNotRegistrar):
		writeError(responseWriter, http.StatusConflict, errMsgRequestNotRegistrar)
	case errors.Is(err, appidentity.ErrAppIDInRegistrarPrefix):
		writeError(responseWriter, http.StatusConflict, errMsgAppIDInRegistrarPrefix)
	case errors.Is(err, appidentity.ErrBindingAppeared):
		writeError(responseWriter, http.StatusConflict, errMsgRegistrationChanged)
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
	case errors.Is(err, appidentity.ErrRequestChanged):
		writeError(responseWriter, http.StatusConflict, errMsgRequestChanged)
	case errors.Is(err, appidentity.ErrAppIDReserved):
		writeError(responseWriter, http.StatusConflict, errMsgAppIDReserved)
	// The registry cases precede the context cases: a registry failure may wrap a deadline.
	case errors.Is(err, appidentity.ErrAppRegistryUnavailable):
		writeError(responseWriter, http.StatusBadGateway, errMsgAppRegistryUnavailable)
	case errors.Is(err, appidentity.ErrAppRegistryNotConfigured):
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgAppRegistryUnavailable)
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
