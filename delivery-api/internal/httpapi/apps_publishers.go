package httpapi

import (
	"net/http"
	"time"
)

// appMintPublisherRequestBody is the body of POST /v1/apps/{app_id}/ntfy-publisher. Unknown fields
// (including app_id: the app comes only from the key) are rejected.
type appMintPublisherRequestBody struct {
	Rotate bool `json:"rotate"`
}

// appPublisherScoped wraps an app-scoped publisher handler: sensitive headers and the nil-Publishers
// 503 run before auth, then the app key must resolve to the {app_id} in the path.
func (server *Server) appPublisherScoped(handler http.HandlerFunc) http.HandlerFunc {
	return server.requirePublishers(server.requireAppKey(assertedAppIDFromPath, handler))
}

// handleAppMintPublisher serves POST /v1/apps/{app_id}/ntfy-publisher (200). The app (re)mints its own
// ntfy publisher token; delivery-api seals it and the response carries only the token-free view.
// rotate=false makes provisioning-api mint an additional token (old ones stay valid), so the route is
// rate limited per app; rotate=true revokes the old tokens first.
func (server *Server) handleAppMintPublisher(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body appMintPublisherRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	started := time.Now()
	view, err := server.publishers.Mint(request.Context(), appID, body.Rotate)
	if err != nil {
		server.writePublisherError(responseWriter, request, err)
		return
	}
	server.logger.Info("app publisher minted",
		"app_id", appID, "rotate", body.Rotate, "duration", time.Since(started).String())
	writeJSON(responseWriter, http.StatusOK, publisherResponseBody{Publisher: view})
}
