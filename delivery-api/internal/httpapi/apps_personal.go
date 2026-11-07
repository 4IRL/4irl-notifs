package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	errMsgPersonalUnavailable = "personal destinations unavailable"

	errMsgPersonalCleanupFailed = "personal destination cleanup failed"
)

// PersonalDestinations is the app-facing personal destination surface the
// /v1/apps/{app_id}/personal-destinations endpoints call. *personal.Service satisfies it. The caller's
// app id always comes from its authenticated key, never from a body.
type PersonalDestinations interface {
	Create(ctx context.Context, appID string, input personal.CreateInput) (personal.AppView, error)
	Lookup(ctx context.Context, appID string, person personal.PersonRef) (string, []personal.AppView, error)
	Update(ctx context.Context, appID string, id int64, person personal.PersonRef, patch personal.Patch) (personal.AppView, error)
	Delete(ctx context.Context, appID string, id int64, person personal.PersonRef) error
	OptIn(ctx context.Context, appID string, id int64, person personal.PersonRef) (personal.AppView, error)
	OptOut(ctx context.Context, appID string, id int64, person personal.PersonRef) (personal.AppView, bool, error)
	// ForgetPersonForApp and ForgetPerson back the local cleanup that runs before a deprovision relay.
	ForgetPersonForApp(ctx context.Context, appID, personHash string) error
	ForgetPerson(ctx context.Context, personHash string) error
	// AdminList, AdminSetEnabled and AdminDelete back the operator routes under /v1/admin.
	AdminList(ctx context.Context, personHash string) ([]personal.AdminView, int, error)
	AdminSetEnabled(ctx context.Context, id int64, enabled bool, reason string) (personal.AdminView, error)
	AdminDelete(ctx context.Context, id int64) error
}

// forgetPersonForApp runs the local personal-destination cleanup of one person for one app before a
// deprovision relay. It returns false after answering 503 when the cleanup failed, so the caller must not
// relay (a retry finishes both). With no service wired it logs a warning and returns true: nothing personal
// can exist without the service.
func (server *Server) forgetPersonForApp(
	responseWriter http.ResponseWriter, request *http.Request, appID, personHash string,
) bool {
	if server.personalDestinations == nil {
		server.logger.Warn("personal destination cleanup skipped: service not wired",
			"path", request.URL.Path, "app_id", appID, "person_hash", personHash)
		return true
	}
	if cleanupErr := server.personalDestinations.ForgetPersonForApp(request.Context(), appID, personHash); cleanupErr != nil {
		server.logger.Error("personal destination cleanup failed; deprovision not relayed",
			"path", request.URL.Path, "app_id", appID, "person_hash", personHash, "error", cleanupErr)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgPersonalCleanupFailed)
		return false
	}
	return true
}

// forgetPerson is forgetPersonForApp for the operator's delete-user: it removes all the person's rows.
func (server *Server) forgetPerson(
	responseWriter http.ResponseWriter, request *http.Request, personHash string,
) bool {
	if server.personalDestinations == nil {
		server.logger.Warn("personal destination cleanup skipped: service not wired",
			"path", request.URL.Path, "person_hash", personHash)
		return true
	}
	if cleanupErr := server.personalDestinations.ForgetPerson(request.Context(), personHash); cleanupErr != nil {
		server.logger.Error("personal destination cleanup failed; delete-user not relayed",
			"path", request.URL.Path, "person_hash", personHash, "error", cleanupErr)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgPersonalCleanupFailed)
		return false
	}
	return true
}

// appPersonalScoped is appScoped for the personal destination routes: 503 when no service is wired (before
// the key check, fail closed), then the app key's app must be the {app_id} in the path.
func (server *Server) appPersonalScoped(handler http.HandlerFunc) http.HandlerFunc {
	return server.requirePersonalDestinations(server.requireAppKey(assertedAppIDFromPath, handler))
}

// requirePersonalDestinations answers 503 before anything else is looked at when no service is configured.
func (server *Server) requirePersonalDestinations(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.personalDestinations == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgPersonalUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// personRefBody is the person selector every app call carries: exactly one of the two (the service
// enforces that and validates them).
type personRefBody struct {
	Email      string `json:"email"`
	PersonHash string `json:"person_hash"`
}

func (body personRefBody) ref() personal.PersonRef {
	return personal.PersonRef{Email: body.Email, PersonHash: body.PersonHash}
}

type createPersonalRequestBody struct {
	personRefBody
	Label   string            `json:"label"`
	Type    string            `json:"type"`
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
	OptIn   *bool             `json:"opt_in"`
}

type patchPersonalRequestBody struct {
	personRefBody
	Label   *string           `json:"label"`
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
	Enabled *bool             `json:"enabled"`
}

type personalResponseBody struct {
	PersonalDestination personal.AppView `json:"personal_destination"`
}

type personalLookupResponseBody struct {
	PersonHash           string             `json:"person_hash"`
	PersonalDestinations []personal.AppView `json:"personal_destinations"`
}

type deletePersonalResponseBody struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

// handleAppCreatePersonal serves POST /v1/apps/{app_id}/personal-destinations (201).
func (server *Server) handleAppCreatePersonal(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body createPersonalRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	view, err := server.personalDestinations.Create(request.Context(), appID, personal.CreateInput{
		Person: body.ref(), Label: body.Label, Type: body.Type,
		Config: body.Config, Secrets: body.Secrets, OptIn: body.OptIn,
	})
	if err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusCreated, personalResponseBody{PersonalDestination: view})
}

// handleAppLookupPersonal serves POST /v1/apps/{app_id}/personal-destinations/lookup.
func (server *Server) handleAppLookupPersonal(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body personRefBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	personHash, views, err := server.personalDestinations.Lookup(request.Context(), appID, body.ref())
	if err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	if views == nil {
		views = []personal.AppView{}
	}
	writeJSON(responseWriter, http.StatusOK, personalLookupResponseBody{PersonHash: personHash, PersonalDestinations: views})
}

// handleAppUpdatePersonal serves PATCH /v1/apps/{app_id}/personal-destinations/{id}.
func (server *Server) handleAppUpdatePersonal(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndPersonalID(responseWriter, request)
	if !ok {
		return
	}
	var body patchPersonalRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	view, err := server.personalDestinations.Update(request.Context(), appID, id, body.ref(), personal.Patch{
		Label: body.Label, Config: body.Config, Secrets: body.Secrets, Enabled: body.Enabled,
	})
	if err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, personalResponseBody{PersonalDestination: view})
}

// handleAppDeletePersonal serves DELETE /v1/apps/{app_id}/personal-destinations/{id}.
func (server *Server) handleAppDeletePersonal(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndPersonalID(responseWriter, request)
	if !ok {
		return
	}
	var body personRefBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if err := server.personalDestinations.Delete(request.Context(), appID, id, body.ref()); err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deletePersonalResponseBody{ID: id, Deleted: true})
}

// handleAppOptInPersonal serves PUT /v1/apps/{app_id}/personal-destinations/{id}/opt-in.
func (server *Server) handleAppOptInPersonal(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndPersonalID(responseWriter, request)
	if !ok {
		return
	}
	var body personRefBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	view, err := server.personalDestinations.OptIn(request.Context(), appID, id, body.ref())
	if err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, personalResponseBody{PersonalDestination: view})
}

// handleAppOptOutPersonal serves DELETE /v1/apps/{app_id}/personal-destinations/{id}/opt-in. An opt-out
// that reaped the row answers {"id", "deleted": true}.
func (server *Server) handleAppOptOutPersonal(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndPersonalID(responseWriter, request)
	if !ok {
		return
	}
	var body personRefBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	view, deleted, err := server.personalDestinations.OptOut(request.Context(), appID, id, body.ref())
	if err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	if deleted {
		writeJSON(responseWriter, http.StatusOK, deletePersonalResponseBody{ID: id, Deleted: true})
		return
	}
	writeJSON(responseWriter, http.StatusOK, personalResponseBody{PersonalDestination: view})
}

// appAndPersonalID returns the authenticated app id and the {id} path value as a canonical positive id.
// A malformed id is a 404 identical to an absent row (no oracle), so it never reaches the service
// (and a malformed id does not reveal whether the body names a valid person).
func (server *Server) appAndPersonalID(
	responseWriter http.ResponseWriter, request *http.Request,
) (string, int64, bool) {
	appID, authenticated := server.authenticatedAppID(responseWriter, request)
	if !authenticated {
		return "", 0, false
	}
	rawID := request.PathValue(pathValueID)
	id, parseErr := strconv.ParseInt(rawID, 10, 64)
	if parseErr != nil || id < 1 || strconv.FormatInt(id, 10) != rawID {
		writeError(responseWriter, http.StatusNotFound, personal.ErrNotFound.Error())
		return "", 0, false
	}
	return appID, id, true
}

// writePersonalError maps a personal-destination error to an HTTP response. Validation messages name
// fields, never values; sentinels map to fixed messages; anything else is logged (path only, never the
// body, which can carry an email or secrets) and answered with a generic 500.
func (server *Server) writePersonalError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	var validationErr *personal.ValidationError
	switch {
	case errors.As(err, &validationErr):
		writeError(responseWriter, http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, personal.ErrNotFound):
		writeError(responseWriter, http.StatusNotFound, personal.ErrNotFound.Error())
	case errors.Is(err, personal.ErrManagedByOtherApp):
		writeError(responseWriter, http.StatusForbidden, personal.ErrManagedByOtherApp.Error())
	case errors.Is(err, personal.ErrDisabledByOperator):
		writeError(responseWriter, http.StatusConflict, personal.ErrDisabledByOperator.Error())
	case errors.Is(err, personal.ErrLimitReached):
		writeError(responseWriter, http.StatusConflict, personal.ErrLimitReached.Error())
	case errors.Is(err, registry.ErrURLCheckUnavailable):
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgURLCheckUnavailable)
	default:
		server.logger.Error("personal destination service error", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	}
}
