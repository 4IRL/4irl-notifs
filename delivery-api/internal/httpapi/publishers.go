package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// Error messages returned in the {"error": "..."} body of publisher responses.
const (
	errMsgPublisherNotFound        = "publisher not found"
	errMsgPublisherMintUnavailable = "publisher minting unavailable"
	errMsgPublisherMintFailed      = "provisioning-api mint failed"
)

// Publishers is the per-app ntfy publisher surface the
// /v1/admin/apps/{app_id}/ntfy-publisher endpoints call. *publisher.Service
// satisfies it. Reading the plaintext token is deliberately not part of it: the
// token is for the ntfy sink, never the admin API.
type Publishers interface {
	Mint(ctx context.Context, appID string) (publisher.PublisherView, error)
	Get(ctx context.Context, appID string) (publisher.PublisherView, error)
}

// publisherResponseBody wraps one publisher view. It never carries a token.
type publisherResponseBody struct {
	Publisher publisher.PublisherView `json:"publisher"`
}

// requirePublishers wraps a publisher handler so that it fails closed: with no
// Publishers configured the request is answered 503 before anything else is
// looked at.
func (server *Server) requirePublishers(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.publishers == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgPublisherMintUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handleMintPublisher serves POST /v1/admin/apps/{app_id}/ntfy-publisher (200).
func (server *Server) handleMintPublisher(responseWriter http.ResponseWriter, request *http.Request) {
	view, err := server.publishers.Mint(request.Context(), request.PathValue("app_id"))
	if err != nil {
		server.writePublisherError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, publisherResponseBody{Publisher: view})
}

// handleGetPublisher serves GET /v1/admin/apps/{app_id}/ntfy-publisher (200).
func (server *Server) handleGetPublisher(responseWriter http.ResponseWriter, request *http.Request) {
	view, err := server.publishers.Get(request.Context(), request.PathValue("app_id"))
	if err != nil {
		server.writePublisherError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, publisherResponseBody{Publisher: view})
}

// writePublisherError is the single chokepoint mapping a publisher error to an
// HTTP response. Validation errors carry a client-safe message; known sentinels
// map to fixed messages; anything else is a provisioning-api mint failure (or a
// store failure), logged with the path only, never the request body, and answered
// with a generic 502 so no upstream detail leaks.
func (server *Server) writePublisherError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	var validationErr *publisher.ValidationError
	switch {
	case errors.As(err, &validationErr):
		writeError(responseWriter, http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, registry.ErrNoPublisher):
		writeError(responseWriter, http.StatusNotFound, errMsgPublisherNotFound)
	case errors.Is(err, publisher.ErrMintUnavailable):
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgPublisherMintUnavailable)
	default:
		server.logger.Error(errMsgPublisherMintFailed, "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusBadGateway, errMsgPublisherMintFailed)
	}
}
