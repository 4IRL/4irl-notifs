package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

// Client-safe messages of the operator provisioning routes.
const (
	errMsgAdminAppNotBound  = "app has no approved binding"
	errMsgAdminBindingCheck = "binding check unavailable"
	errMsgInvalidAppID      = "invalid app_id"
	errMsgUserIDRequired    = "user_id required"
)

// AdminProvisioning is the provisioning-api relay the operator /v1/admin provisioning routes call.
// *provisionclient.Client satisfies it. It is separate from AppProvisioning so the app surface stays
// minimal: the operator names the app in the request.
type AdminProvisioning interface {
	Provision(ctx context.Context, appID string, email string) (provisionclient.Passthrough, error)
	Deprovision(ctx context.Context, appID string, target provisionclient.DeprovisionTarget) (provisionclient.Passthrough, error)
	ResetPassword(ctx context.Context, appID, email string) (provisionclient.Passthrough, error)
	ListUsers(ctx context.Context) (provisionclient.Passthrough, error)
	DeleteUser(ctx context.Context, userID string) (provisionclient.Passthrough, error)
	TestNotify(ctx context.Context, request provisionclient.TestNotifyRequest) (provisionclient.Passthrough, error)
}

// BindingChecker reports whether an app has an approved binding. *appidentity.Service satisfies it. It
// is the structural twin of publisher.BindingChecker.
type BindingChecker interface {
	HasBinding(ctx context.Context, appID string) (bool, error)
}

// requireAdminProvisioning answers 503 before anything else is looked at when no AdminProvisioning is
// configured (fail closed), and marks every response non-cacheable.
func (server *Server) requireAdminProvisioning(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.adminProvisioning == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgProvisioningUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// Operator request bodies: exactly what the admin UI sends today; any other field is a 400.
type adminAppEmailRequestBody struct {
	AppID string `json:"app_id"`
	Email string `json:"email"`
}

type adminDeprovisionRequestBody struct {
	AppID  string `json:"app_id"`
	UserID string `json:"user_id"`
}

type adminTestNotifyRequestBody struct {
	AppID      string   `json:"app_id"`
	Recipients []string `json:"recipients"`
	Channel    string   `json:"channel"`
	Message    string   `json:"message"`
	Title      string   `json:"title"`
}

// validAdminAppID answers 400 and returns false when appID is malformed.
func validAdminAppID(responseWriter http.ResponseWriter, appID string) bool {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidAppID)
		return false
	}
	return true
}

// requireBoundApp refuses an app with no approved binding before any upstream call: a nil checker or a
// lookup error is 503 (fail closed), a confirmed absent binding is 404. The app id must already be valid.
func (server *Server) requireBoundApp(responseWriter http.ResponseWriter, request *http.Request, appID string) bool {
	if server.bindingChecker == nil {
		server.logger.Error("operator binding check unavailable: no checker", "path", request.URL.Path, "app_id", appID)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgAdminBindingCheck)
		return false
	}
	bound, lookupErr := server.bindingChecker.HasBinding(request.Context(), appID)
	if lookupErr != nil {
		server.logger.Error("operator binding check failed",
			"path", request.URL.Path, "app_id", appID, "error", lookupErr)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgAdminBindingCheck)
		return false
	}
	if !bound {
		server.logger.Info("operator action refused: app not bound", "path", request.URL.Path, "app_id", appID)
		writeError(responseWriter, http.StatusNotFound, errMsgAdminAppNotBound)
		return false
	}
	return true
}

// handleAdminProvision serves POST /v1/admin/provision. The app must be bound.
func (server *Server) handleAdminProvision(responseWriter http.ResponseWriter, request *http.Request) {
	var body adminAppEmailRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if !validAdminAppID(responseWriter, body.AppID) {
		return
	}
	if body.Email == "" {
		writeError(responseWriter, http.StatusBadRequest, errMsgEmailRequired)
		return
	}
	if !server.requireBoundApp(responseWriter, request, body.AppID) {
		return
	}
	started := time.Now()
	answer, err := server.adminProvisioning.Provision(request.Context(), body.AppID, body.Email)
	server.relayProvisioning(responseWriter, request, relayCall{
		appID: body.AppID, personHash: personhash.Hash(body.Email), action: "admin-provision", started: started,
	}, answer, err)
}

// handleAdminDeprovision serves POST /v1/admin/deprovision. Not binding-guarded.
func (server *Server) handleAdminDeprovision(responseWriter http.ResponseWriter, request *http.Request) {
	var body adminDeprovisionRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if !validAdminAppID(responseWriter, body.AppID) {
		return
	}
	if !usableUserID(body.UserID) {
		writeError(responseWriter, http.StatusBadRequest, errMsgUserIDRequired)
		return
	}
	loggedHash := ""
	if personHash, isHash := personhash.HashFromNtfyUser(body.UserID); isHash {
		loggedHash = personHash
		if !server.forgetPersonForApp(responseWriter, request, body.AppID, personHash) {
			return
		}
	} else {
		server.logger.Info("personal destination cleanup skipped: user id is not a derived ntfy user",
			"path", request.URL.Path, "app_id", body.AppID)
	}
	started := time.Now()
	answer, err := server.adminProvisioning.Deprovision(request.Context(), body.AppID,
		provisionclient.DeprovisionTarget{UserID: body.UserID})
	server.relayProvisioning(responseWriter, request, relayCall{
		appID: body.AppID, personHash: loggedHash, action: "admin-deprovision", started: started,
	}, answer, err)
}

// handleAdminResetPassword serves POST /v1/admin/reset-password. The operator takes no door slot: it never
// reserves a limit attempt. Not binding-guarded.
func (server *Server) handleAdminResetPassword(responseWriter http.ResponseWriter, request *http.Request) {
	var body adminAppEmailRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if !validAdminAppID(responseWriter, body.AppID) {
		return
	}
	if body.Email == "" {
		writeError(responseWriter, http.StatusBadRequest, errMsgEmailRequired)
		return
	}
	started := time.Now()
	answer, err := server.adminProvisioning.ResetPassword(request.Context(), body.AppID, body.Email)
	server.relayProvisioning(responseWriter, request, relayCall{
		appID: body.AppID, personHash: personhash.Hash(body.Email), action: "admin-reset-password", started: started,
	}, answer, err)
}

// handleAdminListUsers serves GET /v1/admin/users.
func (server *Server) handleAdminListUsers(responseWriter http.ResponseWriter, request *http.Request) {
	started := time.Now()
	answer, err := server.adminProvisioning.ListUsers(request.Context())
	server.relayProvisioning(responseWriter, request, relayCall{action: "admin-list-users", started: started}, answer, err)
}

// usableUserID reports whether id may be sent upstream: non-empty and not a dot segment, which would
// resolve to a different upstream path (/v1/users/. or /v1/users/..).
func usableUserID(id string) bool {
	return id != "" && id != "." && id != ".."
}

// handleAdminDeleteUser serves DELETE /v1/admin/users/{user_id}. The id must be non-empty and not a dot segment.
func (server *Server) handleAdminDeleteUser(responseWriter http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("user_id")
	if !usableUserID(userID) {
		writeError(responseWriter, http.StatusBadRequest, errMsgUserIDRequired)
		return
	}
	loggedHash := ""
	if personHash, isHash := personhash.HashFromNtfyUser(userID); isHash {
		loggedHash = personHash
		if !server.forgetPerson(responseWriter, request, personHash) {
			return
		}
	} else {
		server.logger.Info("personal destination cleanup skipped: user id is not a derived ntfy user",
			"path", request.URL.Path)
	}
	started := time.Now()
	answer, err := server.adminProvisioning.DeleteUser(request.Context(), userID)
	server.relayProvisioning(responseWriter, request, relayCall{
		personHash: loggedHash, action: "admin-delete-user", started: started,
	}, answer, err)
}

// handleAdminTestNotify serves POST /v1/admin/test-notify. The app must be bound.
func (server *Server) handleAdminTestNotify(responseWriter http.ResponseWriter, request *http.Request) {
	var body adminTestNotifyRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if !validAdminAppID(responseWriter, body.AppID) {
		return
	}
	if !server.requireBoundApp(responseWriter, request, body.AppID) {
		return
	}
	started := time.Now()
	answer, err := server.adminProvisioning.TestNotify(request.Context(), provisionclient.TestNotifyRequest{
		AppID: body.AppID, Recipients: body.Recipients, Channel: body.Channel, Message: body.Message, Title: body.Title,
	})
	server.relayProvisioning(responseWriter, request, relayCall{
		appID: body.AppID, action: "admin-test-notify", started: started,
	}, answer, err)
}
