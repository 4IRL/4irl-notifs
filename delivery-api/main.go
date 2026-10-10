// Command delivery-api is the generic delivery service for the 4IRL app
// family. With no arguments it serves HTTP; the "migrate" subcommand applies
// database migrations and exits.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/database"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/httpapi"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/readiness"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/secretenv"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	defaultListenAddress = ":8080"
	readHeaderTimeout    = 10 * time.Second
	idleTimeout          = 60 * time.Second
	shutdownTimeout      = 10 * time.Second
)

// envOrDefault returns the environment value for key, or fallback when unset.
func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	runErr := run(ctx, logger, os.Args[1:])
	stop()

	if runErr != nil {
		logger.Error("delivery-api exited with an error", "error", runErr)
		os.Exit(1)
	}
}

// run dispatches on args: no arguments serves HTTP until ctx is canceled,
// "migrate" applies database migrations, and anything else is an error.
func run(ctx context.Context, logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return runServe(ctx, logger)
	}

	switch args[0] {
	case "migrate":
		return runMigrate(ctx, logger)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// runServe loads the (mandatory) database config first, so a missing or
// invalid credential fails before any port is bound, then serves HTTP until
// ctx is canceled. The pool is opened and closed here, around serve.
func runServe(ctx context.Context, logger *slog.Logger) error {
	dbConfig, configErr := database.ConfigFromEnv(os.Getenv, secretenv.Resolve)
	if configErr != nil {
		return fmt.Errorf("load database config: %w", configErr)
	}

	pool, poolErr := database.NewPool(ctx, dbConfig)
	if poolErr != nil {
		return poolErr
	}
	defer pool.Close()

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		return fmt.Errorf("load migrations: %w", loadErr)
	}
	checker := readiness.NewChecker(pool, migrate.NewPGStore(pool), migrate.ExpectedVersion(loaded))
	handler := httpapi.NewServer(httpapi.ServerConfig{Logger: logger, Readiness: checker}).Handler()

	listenAddress := envOrDefault("LISTEN_ADDRESS", defaultListenAddress)
	listener, listenErr := net.Listen("tcp", listenAddress)
	if listenErr != nil {
		return fmt.Errorf("listen on %s: %w", listenAddress, listenErr)
	}

	logger.Info("delivery-api listening", "address", listener.Addr().String())
	return serve(ctx, logger, listener, handler, shutdownTimeout)
}

// runMigrate applies the embedded migrations and returns the error, so the
// process exit code reflects a failed migration.
func runMigrate(ctx context.Context, logger *slog.Logger) error {
	dbConfig, configErr := database.ConfigFromEnv(os.Getenv, secretenv.Resolve)
	if configErr != nil {
		return fmt.Errorf("load database config: %w", configErr)
	}

	pool, poolErr := database.NewPool(ctx, dbConfig)
	if poolErr != nil {
		return poolErr
	}
	defer pool.Close()

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		return fmt.Errorf("load migrations: %w", loadErr)
	}

	applied, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, logger)
	if runErr != nil {
		return runErr
	}
	logger.Info("migrations complete", "applied", applied, "schema_version", migrate.ExpectedVersion(loaded))
	return nil
}

// serve serves handler on listener until it fails or ctx is canceled. On
// cancellation it shuts the server down gracefully, giving in-flight requests
// up to shutdownTimeout to finish, and returns nil on a clean shutdown or the
// Shutdown error (wrapping context.DeadlineExceeded on a timeout) otherwise.
// The listener and timeout are injected so the function is testable without
// binding LISTEN_ADDRESS or waiting out the production timeout.
func serve(
	ctx context.Context,
	logger *slog.Logger,
	listener net.Listener,
	handler http.Handler,
	shutdownTimeout time.Duration,
) error {
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Buffered so the Serve goroutine can always deliver its final result,
	// even after the ctx.Done() branch has already returned.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down", "timeout", shutdownTimeout.String())
		// Rooted in Background, not ctx: ctx is already canceled, and a
		// context derived from it would give Shutdown no grace period.
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancelShutdown()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			// Shutdown gave up waiting (e.g. the grace period elapsed) but
			// leaves lingering connections open; force-drop them so none
			// outlive serve. The Shutdown error is still returned unchanged.
			_ = httpServer.Close()
		}
		return shutdownErr
	case err := <-serveErr:
		// Serve stopped on its own, independent of ctx, so there is nothing
		// to shut down.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
