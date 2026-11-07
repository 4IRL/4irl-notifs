package httpapi

import (
	"net/http"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
)

const (
	errMsgInvalidPersonalID   = "invalid personal destination id"
	errMsgEnabledRequired     = "enabled is required"
	queryPersonHash           = "person_hash"
	maxAdminPersonalBodyBytes = 4 << 10
)

type adminPersonalListResponseBody struct {
	PersonalDestinations []personal.AdminView `json:"personal_destinations"`
	Total                int                  `json:"total"`
}

type adminPersonalResponseBody struct {
	PersonalDestination personal.AdminView `json:"personal_destination"`
}

type adminPatchPersonalRequestBody struct {
	Enabled        *bool  `json:"enabled"`
	DisabledReason string `json:"disabled_reason"`
}

// handleAdminListPersonal serves GET /v1/admin/personal-destinations?person_hash=.
func (server *Server) handleAdminListPersonal(responseWriter http.ResponseWriter, request *http.Request) {
	views, total, err := server.personalDestinations.AdminList(request.Context(), request.URL.Query().Get(queryPersonHash))
	if err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	if views == nil {
		views = []personal.AdminView{}
	}
	writeJSON(responseWriter, http.StatusOK, adminPersonalListResponseBody{PersonalDestinations: views, Total: total})
}

// handleAdminPatchPersonal serves PATCH /v1/admin/personal-destinations/{id}.
func (server *Server) handleAdminPatchPersonal(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parsePositiveID(responseWriter, request, errMsgInvalidPersonalID)
	if !ok {
		return
	}
	var body adminPatchPersonalRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminPersonalBodyBytes) {
		return
	}
	if body.Enabled == nil {
		writeError(responseWriter, http.StatusBadRequest, errMsgEnabledRequired)
		return
	}
	view, err := server.personalDestinations.AdminSetEnabled(request.Context(), id, *body.Enabled, body.DisabledReason)
	if err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, adminPersonalResponseBody{PersonalDestination: view})
}

// handleAdminDeletePersonal serves DELETE /v1/admin/personal-destinations/{id}.
func (server *Server) handleAdminDeletePersonal(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parsePositiveID(responseWriter, request, errMsgInvalidPersonalID)
	if !ok {
		return
	}
	if err := server.personalDestinations.AdminDelete(request.Context(), id); err != nil {
		server.writePersonalError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deletePersonalResponseBody{ID: id, Deleted: true})
}
