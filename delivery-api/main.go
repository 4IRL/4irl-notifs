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
	"sync"
	"syscall"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/database"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/httpapi"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/readiness"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/secretenv"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ntfyURLEnv names the env var holding the internal ntfy origin (e.g. http://ntfy:80).
	ntfyURLEnv = "DELIVERY_NTFY_URL"
	// provisioningURLEnv names the env var holding the internal provisioning-api origin (e.g. http://provisioning-api:8080).
	provisioningURLEnv = "DELIVERY_PROVISIONING_URL"

	defaultListenAddress = ":8080"
	readHeaderTimeout    = 10 * time.Second
	idleTimeout          = 60 * time.Second

	// shutdownTimeout is the HTTP drain budget. It is deliberately generous (the two drains below actually run
	// in parallel, since the background context is canceled the moment the signal arrives): it covers the
	// 10 s a ?wait request can hold Shutdown (httpapi.MaxWait) plus the 16 s an in-flight delivery attempt can
	// need to finish and be recorded (dispatch.MaxInFlightShutdown), so a SIGTERM never abandons either.
	shutdownTimeout = httpapi.MaxWait + dispatch.MaxInFlightShutdown
	// backgroundStopMargin is slack on top of dispatch.MaxInFlightShutdown for the worker and sweeper to unwind.
	backgroundStopMargin = 4 * time.Second
	// backgroundStopTimeout bounds how long runServe waits for the worker and sweeper after HTTP has drained.
	// Worst-case total shutdown is shutdownTimeout + backgroundStopTimeout (46 s), which the compose
	// stop_grace_period must exceed (docker-compose.yml and docker-compose.prod.yml; pinned by
	// TestComposeStopGracePeriodCoversShutdown).
	backgroundStopTimeout = dispatch.MaxInFlightShutdown + backgroundStopMargin
)

// envOrDefault returns the environment value for key, or fallback when unset.
func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// buildSinks returns the delivery sinks and the destination types message
// submission accepts; both hold exactly the same types, so a type is never
// accepted at submit that the worker could not deliver. The ntfy sink is added
// only when ntfyURL is set; otherwise it is left out of both (fail closed: an
// ntfy destination is rejected at submit rather than dead-lettered later).
func buildSinks(guardedClient *http.Client, ntfyURL string, logger *slog.Logger) (map[string]sink.Sink, map[string]bool) {
	sinks := map[string]sink.Sink{
		registry.TypeWebhook: sink.NewWebhookSink(guardedClient),
		registry.TypeDiscord: sink.NewDiscordSink(guardedClient),
	}
	ntfySink := sink.NewNtfySink(sink.NtfyConfig{BaseURL: ntfyURL})
	if ntfySink.Configured() {
		sinks[registry.TypeNtfy] = ntfySink
	} else {
		logger.Warn("ntfy sink disabled: " + ntfyURLEnv + " unset")
	}

	supportedTypes := make(map[string]bool, len(sinks))
	for destinationType := range sinks {
		supportedTypes[destinationType] = true
	}
	return sinks, supportedTypes
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

// openDatabase loads the database config, opens the pool and loads the embedded
// migrations. The caller owns closing the returned pool.
func openDatabase(ctx context.Context) (*pgxpool.Pool, []migrate.Migration, error) {
	dbConfig, configErr := database.ConfigFromEnv(os.Getenv, secretenv.Resolve)
	if configErr != nil {
		return nil, nil, fmt.Errorf("load database config: %w", configErr)
	}

	pool, poolErr := database.NewPool(ctx, dbConfig)
	if poolErr != nil {
		return nil, nil, poolErr
	}

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("load migrations: %w", loadErr)
	}
	return pool, loaded, nil
}

// runServe loads the (mandatory) database config, master key and retention
// window first, so a missing or invalid value fails before any port is bound,
// then serves HTTP and runs the delivery worker and retention sweeper until ctx
// is canceled. The pool is opened and closed here, around serve and the
// background work: the deferred Close runs only after both have stopped.
func runServe(ctx context.Context, logger *slog.Logger) error {
	pool, loaded, openErr := openDatabase(ctx)
	if openErr != nil {
		return openErr
	}
	defer pool.Close()

	// The master key and retention window are resolved before net.Listen so a
	// missing or malformed value fails the process before any port is bound.
	sealer, sealErr := seal.FromEnv(secretenv.Resolve)
	if sealErr != nil {
		return sealErr
	}
	retention, retentionErr := dispatch.RetentionFromEnv(os.Getenv)
	if retentionErr != nil {
		return retentionErr
	}

	checker := readiness.NewChecker(pool, migrate.NewPGStore(pool), migrate.ExpectedVersion(loaded))
	// See ServerConfig.Registry for why it is set only here.
	registryService := registry.NewService(registry.NewPGStore(pool), sealer, logger)

	// One transport client per process: it owns the connection pool and the SSRF guard. Production passes
	// the zero Config, so the address policy and TLS settings are the guarded defaults.
	guardedClient := transport.New(transport.Config{})
	messageStore := message.NewPGStore(pool)
	sinks, supportedTypes := buildSinks(guardedClient, envOrDefault(ntfyURLEnv, ""), logger)
	worker := dispatch.NewWorker(dispatch.WorkerConfig{
		Queue:    messageStore,
		Resolver: registryService,
		Sinks:    sinks,
		Logger:   logger,
	})
	messageService := message.NewService(message.ServiceConfig{
		Store:          messageStore,
		Resolver:       registryService,
		SupportedTypes: supportedTypes,
		Wake:           worker.Wake,
		Logger:         logger,
	})
	// The minter is passed only when a provisioning-api origin is configured: a nil interface (not a typed-nil
	// *provisionclient.Client) makes publisher.Service.Mint answer ErrMintUnavailable.
	var minter publisher.Minter
	provisionClient := provisionclient.New(provisionclient.Config{BaseURL: envOrDefault(provisioningURLEnv, "")})
	if provisionClient.Configured() {
		minter = provisionClient
	} else {
		logger.Warn("publisher minting disabled: " + provisioningURLEnv + " unset")
	}
	publisherService := publisher.NewService(publisher.NewPGStore(pool), minter, sealer)
	// See ServerConfig.Messages and ServerConfig.Publishers for why they are set only here.
	handler := httpapi.NewServer(httpapi.ServerConfig{
		Logger:     logger,
		Readiness:  checker,
		Registry:   registryService,
		Messages:   messageService,
		Publishers: publisherService,
	}).Handler()

	listenAddress := envOrDefault("LISTEN_ADDRESS", defaultListenAddress)
	listener, listenErr := net.Listen("tcp", listenAddress)
	if listenErr != nil {
		return fmt.Errorf("listen on %s: %w", listenAddress, listenErr)
	}

	background := startBackground(ctx,
		worker.Run,
		func(backgroundCtx context.Context) {
			dispatch.RunSweeper(backgroundCtx, dispatch.SweeperConfig{
				Expirer:   messageStore,
				Retention: retention,
				Interval:  dispatch.DefaultSweepInterval,
				Logger:    logger,
			})
		},
	)

	logger.Info("delivery-api listening", "address", listener.Addr().String())
	serveErr := serve(ctx, logger, listener, handler, shutdownTimeout)
	// Stop the worker and sweeper whether serve returned on cancellation or on a listener failure, and wait
	// for them before the deferred pool.Close so an in-flight attempt can still record its outcome.
	background.stop(logger, backgroundStopTimeout)
	return serveErr
}

// backgroundTasks tracks goroutines started by startBackground so they can be canceled and awaited together.
type backgroundTasks struct {
	cancel context.CancelFunc
	group  sync.WaitGroup
}

// startBackground runs every function in its own goroutine with a context derived from ctx, so canceling
// ctx (a signal) or calling stop cancels them all. The WaitGroup Add happens here, before each goroutine
// launches: adding inside the goroutine would race with stop's Wait.
func startBackground(ctx context.Context, runners ...func(context.Context)) *backgroundTasks {
	backgroundCtx, cancel := context.WithCancel(ctx)
	started := &backgroundTasks{cancel: cancel}
	for _, runner := range runners {
		started.group.Add(1)
		go func() {
			defer started.group.Done()
			runner(backgroundCtx)
		}()
	}
	return started
}

// stop cancels the background context and waits up to timeout for every goroutine to return, logging an
// error instead of blocking forever when one does not. Calling it more than once is safe.
func (started *backgroundTasks) stop(logger *slog.Logger, timeout time.Duration) {
	started.cancel()

	stopped := make(chan struct{})
	go func() {
		started.group.Wait()
		close(stopped)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-stopped:
	case <-timer.C:
		logger.Error("background workers did not stop in time", "timeout", timeout.String())
	}
}

// runMigrate applies the embedded migrations and returns the error, so the
// process exit code reflects a failed migration.
func runMigrate(ctx context.Context, logger *slog.Logger) error {
	pool, loaded, openErr := openDatabase(ctx)
	if openErr != nil {
		return openErr
	}
	defer pool.Close()

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
	case serveResultErr := <-serveErr:
		// Serve stopped on its own, independent of ctx, so there is nothing
		// to shut down.
		if errors.Is(serveResultErr, http.ErrServerClosed) {
			return nil
		}
		return serveResultErr
	}
}
