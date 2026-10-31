package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

// Client-safe messages of the app-scoped provisioning routes.
const (
	errMsgProvisioningUnavailable = "provisioning unavailable"
	errMsgProvisioningFailed      = "provisioning request failed"
	errMsgEmailRequired           = "email required"
	errMsgPasswordRequired        = "password required"
	errMsgEmailOrHashRequired     = "exactly one of email or person_hash required"
	errMsgInvalidPersonHash       = "invalid person_hash"
)

// retryAfterPattern is the only Retry-After shape relayed to an app: a short delay in whole seconds. An
// upstream value of any other shape is dropped rather than reflected.
var retryAfterPattern = regexp.MustCompile(`^[0-9]{1,9}$`)

// AppProvisioning is the provisioning-api relay the app-scoped provision and deprovision routes call.
// *provisionclient.Client satisfies it. The app id is always the calling app's own (from its key).
type AppProvisioning interface {
	Provision(ctx context.Context, appID string, email string) (provisionclient.Passthrough, error)
	Deprovision(ctx context.Context, appID string, target provisionclient.DeprovisionTarget) (provisionclient.Passthrough, error)
	SetPassword(ctx context.Context, appID, email, password string) (provisionclient.Passthrough, error)
}

// appProvisioningScoped wraps an app-scoped provisioning handler: the response is marked non-cacheable, the
// request answers 503 when no AppProvisioning is configured, and the app key must resolve to the {app_id} in
// the path (a different asserted app is a 403) before the handler runs.
func (server *Server) appProvisioningScoped(handler http.HandlerFunc) http.HandlerFunc {
	return server.requireAppProvisioning(server.requireAppKey(assertedAppIDFromPath, handler))
}

// requireAppProvisioning answers 503 before anything else is looked at when no AppProvisioning is
// configured (fail closed).
func (server *Server) requireAppProvisioning(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.appProvisioning == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgProvisioningUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

type appProvisionRequestBody struct {
	Email string `json:"email"`
}

type appDeprovisionRequestBody struct {
	Email      string `json:"email"`
	PersonHash string `json:"person_hash"`
}

// handleAppProvision serves POST /v1/apps/{app_id}/provision.
func (server *Server) handleAppProvision(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body appProvisionRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if body.Email == "" {
		writeError(responseWriter, http.StatusBadRequest, errMsgEmailRequired)
		return
	}
	started := time.Now()
	answer, err := server.appProvisioning.Provision(request.Context(), appID, body.Email)
	server.relayProvisioning(responseWriter, request, relayCall{
		appID: appID, personHash: personhash.Hash(body.Email), action: "provision", started: started,
	}, answer, err)
}

// handleAppDeprovision serves POST /v1/apps/{app_id}/deprovision.
func (server *Server) handleAppDeprovision(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body appDeprovisionRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if (body.Email == "") == (body.PersonHash == "") {
		writeError(responseWriter, http.StatusBadRequest, errMsgEmailOrHashRequired)
		return
	}
	var target provisionclient.DeprovisionTarget
	var loggedHash string
	if body.Email != "" {
		target = provisionclient.DeprovisionTarget{Email: body.Email}
		loggedHash = personhash.Hash(body.Email)
	} else {
		if !personhash.IsHash(body.PersonHash) {
			writeError(responseWriter, http.StatusBadRequest, errMsgInvalidPersonHash)
			return
		}
		target = provisionclient.DeprovisionTarget{UserID: personhash.NtfyUserFromHash(body.PersonHash)}
		loggedHash = body.PersonHash
	}
	started := time.Now()
	answer, err := server.appProvisioning.Deprovision(request.Context(), appID, target)
	server.relayProvisioning(responseWriter, request, relayCall{
		appID: appID, personHash: loggedHash, action: "deprovision", started: started,
	}, answer, err)
}

type appSetPasswordRequestBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleAppSetPassword serves POST /v1/apps/{app_id}/set-password. Legacy: not for new apps. It sets the
// person's one shared password across apps; provisioning-api refuses (404) when the person is not in this
// app. Only presence is checked here; length and email format are validated upstream and relayed. The
// email and password are never logged.
func (server *Server) handleAppSetPassword(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body appSetPasswordRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if body.Email == "" {
		writeError(responseWriter, http.StatusBadRequest, errMsgEmailRequired)
		return
	}
	if body.Password == "" {
		writeError(responseWriter, http.StatusBadRequest, errMsgPasswordRequired)
		return
	}
	started := time.Now()
	answer, err := server.appProvisioning.SetPassword(request.Context(), appID, body.Email, body.Password)
	server.relayProvisioning(responseWriter, request, relayCall{
		appID: appID, personHash: personhash.Hash(body.Email), action: "set-password", started: started,
	}, answer, err)
}

// relayCall is the non-secret context of one provisioning call, used for logging.
type relayCall struct {
	appID      string
	personHash string
	action     string
	started    time.Time
}

// relayProvisioning is the single response chokepoint for both routes. It never logs the email or the
// upstream body (a provision answer carries the token and possibly the password).
func (server *Server) relayProvisioning(
	responseWriter http.ResponseWriter, request *http.Request, call relayCall,
	answer provisionclient.Passthrough, err error,
) {
	if err != nil {
		server.writeAppProvisioningError(responseWriter, request, call, err)
		return
	}
	if !relayableProvisioningStatus(answer.Status) {
		// 401, 403 and redirects are Access/credential rejections of delivery-api itself, never an app-level
		// answer; any status outside 2xx-5xx is not a valid answer either (fail closed).
		server.logger.Error("provisioning upstream rejected the request",
			"path", request.URL.Path, "app_id", call.appID, "person_hash", call.personHash,
			"action", call.action, "upstream_status", answer.Status)
		writeError(responseWriter, http.StatusBadGateway, errMsgProvisioningFailed)
		return
	}
	server.logger.Info("provisioning call relayed",
		"app_id", call.appID, "person_hash", call.personHash, "action", call.action,
		"upstream_status", answer.Status, "duration", time.Since(call.started).String())
	// Content-Type is forced: the upstream value is untrusted. Cache-Control stays as the wrapper set it.
	responseWriter.Header().Set("Content-Type", "application/json")
	if retryAfterPattern.MatchString(answer.RetryAfter) {
		responseWriter.Header().Set("Retry-After", answer.RetryAfter)
	}
	responseWriter.WriteHeader(answer.Status)
	_, _ = responseWriter.Write(answer.Body)
}

// relayableProvisioningStatus reports whether an upstream status is an app-level answer that may be relayed.
func relayableProvisioningStatus(status int) bool {
	isAnswerRange := (status >= 200 && status < 300) || (status >= 400 && status < 600)
	return isAnswerRange && status != http.StatusUnauthorized && status != http.StatusForbidden
}

// writeAppProvisioningError maps a failure to get an answer from provisioning-api to a response.
func (server *Server) writeAppProvisioningError(
	responseWriter http.ResponseWriter, request *http.Request, call relayCall, err error,
) {
	switch {
	case errors.Is(err, provisionclient.ErrNotConfigured):
		server.logger.Error("provisioning client not configured",
			"path", request.URL.Path, "app_id", call.appID, "person_hash", call.personHash, "action", call.action)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgProvisioningUnavailable)
	case errors.Is(err, context.Canceled):
		server.logger.Debug("provisioning request canceled",
			"path", request.URL.Path, "app_id", call.appID, "person_hash", call.personHash)
		writeError(responseWriter, http.StatusBadGateway, errMsgProvisioningFailed)
	default:
		server.logger.Error("provisioning request failed",
			"path", request.URL.Path, "app_id", call.appID, "person_hash", call.personHash,
			"action", call.action, "duration", time.Since(call.started).String(), "error", err)
		writeError(responseWriter, http.StatusBadGateway, errMsgProvisioningFailed)
	}
}
