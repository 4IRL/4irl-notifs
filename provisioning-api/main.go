// Command provisioning-api is a barebones HTTP service that provisions and
// deprovisions ntfy users, topic ACLs, and access tokens for the 4IRL app
// family by shelling out to the documented ntfy CLI.
//
// It ships in the same image as a bundled ntfy binary and shares ntfy's auth
// database over a docker volume; NTFY_AUTH_FILE / NTFY_AUTH_DEFAULT_ACCESS
// point the CLI at that database without needing the ntfy server running.
package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/httpapi"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personsvc"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

const (
	defaultListenAddress = ":8080"
	defaultNtfyBinary    = "ntfy"
	readHeaderTimeout    = 10 * time.Second
)

// envOrDefault returns the environment value for key, or fallback when unset.
func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	listenAddress := envOrDefault("LISTEN_ADDRESS", defaultListenAddress)
	ntfyBinaryPath := envOrDefault("NTFY_BIN", defaultNtfyBinary)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ntfyClient := ntfycli.NewClient(ntfycli.ClientConfig{
		Runner: ntfycli.ExecRunner{BinaryPath: ntfyBinaryPath},
	})

	// The person-service dual-write is Wave-2/best-effort and optional: an
	// empty PERSON_SERVICE_URL (the local dev stack, which has no Worker)
	// leaves personClient.Configured() false, so Provision skips the
	// dual-write entirely.
	personServiceURL := os.Getenv("PERSON_SERVICE_URL")
	personClient := personsvc.NewClient(personsvc.Config{
		BaseURL:            personServiceURL,
		AccessClientID:     os.Getenv("PERSON_SERVICE_ACCESS_CLIENT_ID"),
		AccessClientSecret: os.Getenv("PERSON_SERVICE_ACCESS_CLIENT_SECRET"),
	})
	logger.Info("person-service dual-write", "enabled", personClient.Configured(), "url", personServiceURL)

	service := provisioning.NewService(provisioning.ServiceConfig{
		Client:           ntfyClient,
		GeneratePassword: provisioning.GenerateRandomPassword,
		PersonClient:     personClient,
		Logger:           logger,
	})
	server := httpapi.NewServer(httpapi.ServerConfig{Service: service, Logger: logger})

	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           server.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	logger.Info("provisioning-api listening", "address", listenAddress, "ntfy_binary", ntfyBinaryPath)
	if serveErr := httpServer.ListenAndServe(); serveErr != nil {
		log.Fatalf("provisioning-api server exited: %v", serveErr)
	}
}
