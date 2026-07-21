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
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/secretenv"
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

// resolveSecretEnv resolves a secret from <key>_FILE (a Docker Compose secret
// file) or the plain <key> env var. A set-but-unreadable file is a deploy
// misconfiguration; because the person-service dual-write is best-effort and
// must never take down core provisioning, it is logged and treated as unset
// (an empty credential simply sends no Access headers, so the dual-write fails
// closed and is swallowed by the caller) rather than crashing startup.
func resolveSecretEnv(logger *slog.Logger, key string) string {
	value, resolveErr := secretenv.Resolve(key)
	if resolveErr != nil {
		logger.Warn("could not read secret file; treating credential as unset", "key", key, "error", resolveErr)
	}
	return value
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
	// dual-write entirely. The Access Service-Token credentials are delivered
	// in prod as Docker Compose secrets (files under /run/secrets), read via
	// the <KEY>_FILE convention; plain <KEY> env vars still work for local dev.
	personServiceURL := os.Getenv("PERSON_SERVICE_URL")
	accessClientID := resolveSecretEnv(logger, "PERSON_SERVICE_ACCESS_CLIENT_ID")
	accessClientSecret := resolveSecretEnv(logger, "PERSON_SERVICE_ACCESS_CLIENT_SECRET")
	personClient := personsvc.NewClient(personsvc.Config{
		BaseURL:            personServiceURL,
		AccessClientID:     accessClientID,
		AccessClientSecret: accessClientSecret,
	})
	logger.Info("person-service dual-write",
		"enabled", personClient.Configured(),
		"url", personServiceURL,
		"auth_configured", accessClientID != "" && accessClientSecret != "",
	)

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
