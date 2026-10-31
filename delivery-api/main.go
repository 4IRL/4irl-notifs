// Command delivery-api is the generic delivery service for the 4IRL app
// family. With no arguments it serves HTTP. The subcommands run once and exit:
// "migrate" applies database migrations, "rotate-keys" re-seals every stored
// secret under DELIVERY_MASTER_KEY (opening old values with
// DELIVERY_MASTER_KEY_PREVIOUS) and "verify-keys" checks that every stored
// secret opens under DELIVERY_MASTER_KEY alone.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/database"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/httpapi"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/keyrotation"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personsvc"
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
	// internalProvisioningHost is the only host provisioningURLEnv may name: the compose service name of provisioning-api.
	internalProvisioningHost = "provisioning-api"
	// provisioningCredentialEnv names the secret holding the internal credential provisioning-api requires
	// (resolved through secretenv, so DELIVERY_PROVISIONING_CREDENTIAL_FILE works in production).
	provisioningCredentialEnv = "DELIVERY_PROVISIONING_CREDENTIAL"
	// personServiceURLEnv names the env var holding the person-service origin used to register an approved app.
	personServiceURLEnv = "PERSON_SERVICE_URL"
	// personServiceAccessIDEnv and personServiceAccessSecretEnv name the Cloudflare Access service token secrets
	// (resolved through secretenv, so the _FILE forms work in production).
	personServiceAccessIDEnv     = "PERSON_SERVICE_ACCESS_CLIENT_ID"
	personServiceAccessSecretEnv = "PERSON_SERVICE_ACCESS_CLIENT_SECRET"

	defaultListenAddress = ":8080"
	readHeaderTimeout    = 10 * time.Second
	idleTimeout          = 60 * time.Second

	// shutdownTimeout is the HTTP drain budget. It is deliberately generous (the two drains below actually run
	// in parallel, since the background context is canceled the moment the signal arrives): it covers the
	// 10 s a ?wait request can hold Shutdown (httpapi.MaxWait) plus the 16 s an in-flight delivery attempt can
	// need to finish and be recorded (dispatch.MaxInFlightShutdown), so a SIGTERM never abandons either.
	shutdownTimeout = httpapi.MaxWait + dispatch.MaxInFlightShutdown
	// backgroundStopMargin is slack on top of dispatch.MaxInFlightShutdown for the worker, sweeper, releaser, heartbeat checker, heartbeat prober and OIDC replay cleanup to unwind.
	backgroundStopMargin = 4 * time.Second
	// backgroundStopTimeout bounds how long runServe waits for the worker, sweeper, releaser, heartbeat checker, heartbeat prober (heartbeat.MaxProberShutdown, pinned by
	// TestProberShutdownFitsBackgroundStopTimeout) and OIDC replay cleanup after HTTP has drained.
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

// provisionInterfaces maps the provisioning client to the interfaces that consume it. Each is set only
// when the client exists: an untyped nil (never a typed-nil *provisionclient.Client) keeps the 503 /
// publisher.ErrMintUnavailable guards working.
func provisionInterfaces(client *provisionclient.Client) (publisher.Minter, httpapi.AppProvisioning) {
	if client == nil {
		return nil, nil
	}
	return client, client
}

// buildProvisionClient returns the provisioning-api client, or a nil client when it is unavailable. It needs
// both the provisioning-api origin and the internal credential: with either missing it returns nil and logs
// one WARN naming the missing variable. A set URL whose host is not the compose service name of
// provisioning-api (internalProvisioningHost) is a startup error, so the credential is never sent to
// another origin. The credential value is never logged.
func buildProvisionClient(provisioningURL string, resolveSecret func(string) (string, error), logger *slog.Logger) (*provisionclient.Client, error) {
	if provisioningURL == "" {
		logger.Warn("publisher minting disabled: " + provisioningURLEnv + " unset")
		return nil, nil
	}
	if hostErr := checkInternalProvisioningURL(provisioningURL); hostErr != nil {
		return nil, hostErr
	}
	rawCredential, resolveErr := resolveSecret(provisioningCredentialEnv)
	if resolveErr != nil {
		// The resolver error names the variable and the file path, never the secret value.
		logger.Warn("publisher minting disabled: "+provisioningCredentialEnv+" unreadable", "error", resolveErr)
		return nil, nil
	}
	// A plain env var is not trimmed by secretenv; a stray space or newline would make every mint fail
	// with a header or 401 error that gives no hint of the cause.
	credential := strings.TrimSpace(rawCredential)
	if credential == "" {
		logger.Warn("publisher minting disabled: " + provisioningCredentialEnv + " unset or empty")
		return nil, nil
	}
	return provisionclient.New(provisionclient.Config{BaseURL: provisioningURL, Credential: credential}), nil
}

// buildRegistrar returns the people-directory registrar used when the operator approves an app, or a nil
// interface (never a typed-nil *personsvc.Client, which would bypass the fail-closed nil guard) when
// PERSON_SERVICE_URL is unset. Approval then answers 503. One WARN names each missing or unreadable
// variable, never its value.
func buildRegistrar(personServiceURL string, resolveSecret func(string) (string, error), logger *slog.Logger) appidentity.AppRegistrar {
	personServiceURL = strings.TrimSpace(personServiceURL)
	if personServiceURL == "" {
		logger.Warn("app registration on approval disabled: " + personServiceURLEnv + " unset")
		return nil
	}
	resolve := func(name string) string {
		raw, resolveErr := resolveSecret(name)
		if resolveErr != nil {
			// The resolver error names the variable and the file path, never the secret value.
			logger.Warn("person-service access credential unreadable: "+name, "error", resolveErr)
			return ""
		}
		return strings.TrimSpace(raw)
	}
	accessID := resolve(personServiceAccessIDEnv)
	accessSecret := resolve(personServiceAccessSecretEnv)
	if strings.HasPrefix(personServiceURL, "https://") && (accessID == "" || accessSecret == "") {
		logger.Warn("person-service URL is https but a Cloudflare Access credential is empty: " +
			personServiceAccessIDEnv + " / " + personServiceAccessSecretEnv)
	}
	client := personsvc.NewClient(personsvc.Config{
		BaseURL: personServiceURL, AccessClientID: accessID, AccessClientSecret: accessSecret, Logger: logger,
	})
	return client
}

// Compile-time check that the production client satisfies the approval registrar.
var _ appidentity.AppRegistrar = (*personsvc.Client)(nil)

// checkInternalProvisioningURL accepts only an http(s) URL, without userinfo, whose host is the compose
// service name of provisioning-api. Plain http is allowed because the origin is on the compose network.
// The URL text is not echoed in the error, since a misconfigured value could carry userinfo.
func checkInternalProvisioningURL(rawURL string) error {
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("%s must be an http(s) origin without credentials", provisioningURLEnv)
	}
	if parsed.Hostname() != internalProvisioningHost {
		return fmt.Errorf("%s host must be %q so the internal credential is never sent to another origin",
			provisioningURLEnv, internalProvisioningHost)
	}
	return nil
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
	case "rotate-keys", "verify-keys":
		// Keys come only from the environment or secret files; an argument is rejected without
		// being echoed, so a key mistakenly passed on the command line never reaches a log.
		if len(args) > 1 {
			return fmt.Errorf("%s takes no arguments", args[0])
		}
		if args[0] == "rotate-keys" {
			return runRotateKeys(ctx, logger)
		}
		return runVerifyKeys(ctx, logger)
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
// then serves HTTP and runs the delivery worker and retention sweeper, scheduled-message releaser and heartbeat checker until ctx
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
	maxScheduled, maxScheduledErr := message.MaxScheduledFromEnv(os.Getenv)
	if maxScheduledErr != nil {
		return maxScheduledErr
	}
	maxScheduledTotal, maxScheduledTotalErr := message.MaxScheduledTotalFromEnv(os.Getenv)
	if maxScheduledTotalErr != nil {
		return maxScheduledTotalErr
	}
	// Fail-closed OIDC config: a static dev JWKS file without DELIVERY_OIDC_DEV_MODE=1 stops startup.
	oidcConfig, oidcErr := appidentity.ConfigFromEnv(os.Getenv)
	if oidcErr != nil {
		return oidcErr
	}
	logger.Info("app identity configured",
		"oidc_audience", oidcConfig.Audience, "oidc_dev_mode", oidcConfig.DevMode, "oidc_static_jwks", oidcConfig.JWKSFile != "",
		"pending_request_ttl", oidcConfig.PendingRequestTTL)
	// Built before any port is bound so an unreadable dev JWKS file stops startup. In production no JWKS
	// file is set and the verifier fetches GitHub's keys lazily, on the first token it sees.
	oidcVerifier, verifierErr := appidentity.NewVerifier(appidentity.VerifierConfig{
		Audience: oidcConfig.Audience,
		JWKSFile: oidcConfig.JWKSFile,
		Logger:   logger,
	})
	if verifierErr != nil {
		return fmt.Errorf("build oidc verifier: %w", verifierErr)
	}
	appIdentityStore := appidentity.NewPGStore(pool)
	appIdentityService := appidentity.NewService(appidentity.ServiceConfig{
		Store:    appIdentityStore,
		Admin:    appIdentityStore,
		Verifier: oidcVerifier,
		// A nil interface when unconfigured: approval fails closed with 503.
		Registrar: buildRegistrar(os.Getenv(personServiceURLEnv), secretenv.Resolve, logger),
		Logger:    logger,
	})

	checker := readiness.NewChecker(pool, migrate.NewPGStore(pool), migrate.ExpectedVersion(loaded))
	// See ServerConfig.Registry for why it is set only here.
	// The URL checker is the registration-time SSRF check for app-scoped destination writes: it resolves an
	// app-supplied URL's host (net.DefaultResolver, 3 s timeout) and refuses any non-public address.
	registryService := registry.NewService(registry.NewPGStore(pool), sealer, logger,
		registry.WithURLChecker(registry.NewURLChecker(nil, logger)))

	// One transport client per process: it owns the connection pool and the SSRF guard. Production passes
	// the zero Config, so the address policy and TLS settings are the guarded defaults.
	guardedClient := transport.New(transport.Config{})
	messageStore := message.NewPGStore(pool)
	sinks, supportedTypes := buildSinks(guardedClient, envOrDefault(ntfyURLEnv, ""), logger)
	provisionClient, provisionClientErr := buildProvisionClient(envOrDefault(provisioningURLEnv, ""), secretenv.Resolve, logger)
	if provisionClientErr != nil {
		return provisionClientErr
	}
	minter, appProvisioning := provisionInterfaces(provisionClient)
	publisherStore := publisher.NewPGStore(pool)
	publisherService := publisher.NewService(publisherStore, minter, sealer, logger)
	// Recipients is set only when the ntfy sink is configured: a nil interface (not a typed-nil
	// *publisher.Service) makes the worker dead-letter a recipient delivery as no_publisher instead of sending.
	var recipients dispatch.RecipientResolver
	if _, ntfyConfigured := sinks[registry.TypeNtfy]; ntfyConfigured {
		recipients = publisherService
	}
	worker := dispatch.NewWorker(dispatch.WorkerConfig{
		Queue:      messageStore,
		Resolver:   registryService,
		Recipients: recipients,
		Sinks:      sinks,
		Logger:     logger,
	})
	messageService := message.NewService(message.ServiceConfig{
		Store:              messageStore,
		Resolver:           registryService,
		Publishers:         publisherService,
		SupportedTypes:     supportedTypes,
		Wake:               worker.Wake,
		Logger:             logger,
		MaxScheduledPerApp: maxScheduled,
		MaxScheduledTotal:  maxScheduledTotal,
	})
	// The checker reads the message service as its alert sender, and its freshness tracker backs /readyz.
	heartbeatFreshness := heartbeat.NewFreshness(time.Now)
	heartbeatStore := heartbeat.NewPGStore(pool, sealer)
	heartbeatService := heartbeat.NewService(heartbeat.ServiceConfig{
		Store:    heartbeatStore,
		Messages: messageService,
		Logger:   logger,
	})
	prober := newProberWiring(heartbeatStore, heartbeatService.HoldOffUntil, guardedClient, sealer, logger)
	// See ServerConfig.Messages and ServerConfig.Publishers for why they are set only here.
	handler := httpapi.NewServer(httpapi.ServerConfig{
		Logger:           logger,
		Readiness:        checker,
		HeartbeatChecker: heartbeatFreshness,
		HeartbeatProber:  prober.freshness,
		Registry:         registryService,
		AppRegistry:      registryService,
		Messages:         messageService,
		Publishers:       publisherService,
		Heartbeats:       heartbeatService,
		AppHeartbeats:    heartbeatService,
		AppProvisioning:  appProvisioning,
		AppIdentity:      appIdentityService,
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
		func(backgroundCtx context.Context) {
			dispatch.RunReleaser(backgroundCtx, dispatch.ReleaserConfig{
				Releaser: messageStore,
				Interval: dispatch.DefaultReleaseInterval,
				Wake:     worker.Wake,
				Logger:   logger,
			})
		},
		func(backgroundCtx context.Context) {
			heartbeat.RunChecker(backgroundCtx, heartbeat.CheckerConfig{
				Service:   heartbeatService,
				Interval:  heartbeat.DefaultCheckerInterval,
				Logger:    logger,
				Freshness: heartbeatFreshness,
			})
		},
		prober.run,
		// The OIDC replay cleanup loop hosts the identity-table sweeps and the publisher mint-attempt
		// sweep: different tables on different criteria than the message retention sweeper above.
		func(backgroundCtx context.Context) {
			appidentity.RunReplayCleanup(backgroundCtx, appidentity.ReplayCleanupConfig{
				Store:         appIdentityStore,
				RejectedStore: appIdentityStore,
				PendingStore:  appIdentityStore,
				PendingTTL:    oidcConfig.PendingRequestTTL,
				MintStore:     publisherStore,
				MintWindow:    publisher.MintRateWindow,
				Interval:      appidentity.DefaultReplayCleanupInterval,
				Logger:        logger,
			})
		},
	)

	logger.Info("delivery-api listening", "address", listener.Addr().String())
	serveErr := serve(ctx, logger, listener, handler, shutdownTimeout)
	// Stop the worker, sweeper, releaser, heartbeat checker and OIDC replay cleanup whether serve returned on cancellation or on a listener failure, and wait
	// for them before the deferred pool.Close so an in-flight attempt can still record its outcome.
	background.stop(logger, backgroundStopTimeout)
	return serveErr
}

// proberWiring bundles the HTTP-probe prober's own freshness tracker (it backs /readyz separately from the
// checker's) with its configuration and background runner, so runServe wires both from one place.
type proberWiring struct {
	freshness *heartbeat.Freshness
	config    heartbeat.ProberConfig
	run       func(context.Context)
}

// newProberWiring builds the prober configuration with the production interval and concurrency and a fresh
// freshness tracker.
func newProberWiring(
	store heartbeat.ProbeStore,
	holdOffUntil func() time.Time,
	client *http.Client,
	sealer *seal.Sealer,
	logger *slog.Logger,
) proberWiring {
	freshness := heartbeat.NewFreshness(time.Now)
	config := heartbeat.ProberConfig{
		Store:        store,
		HoldOffUntil: holdOffUntil,
		Client:       client,
		Sealer:       sealer,
		Interval:     heartbeat.DefaultProberInterval,
		Concurrency:  heartbeat.DefaultProberConcurrency,
		Logger:       logger,
		Freshness:    freshness,
	}
	return proberWiring{
		freshness: freshness,
		config:    config,
		run: func(backgroundCtx context.Context) {
			heartbeat.RunProber(backgroundCtx, config)
		},
	}
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

// runRotateKeys re-seals every stored secret under the new master key. The keys
// are resolved before the database is opened, so a missing or bad key fails
// without any database work.
func runRotateKeys(ctx context.Context, logger *slog.Logger) error {
	sealer, sealErr := seal.FromEnv(secretenv.Resolve)
	if sealErr != nil {
		return sealErr
	}
	if !sealer.HasPrevious() {
		return errors.New("rotate-keys requires " + seal.EnvMasterKeyPrevious + " (the old key)")
	}

	pool, _, openErr := openDatabase(ctx)
	if openErr != nil {
		return openErr
	}
	defer pool.Close()

	return rotateKeysOn(ctx, logger, pool, sealer)
}

// rotateKeysOn runs the rotation against an already-open pool; the returned error
// (including keyrotation.ErrIncomplete) is the process exit status. It is split
// out so tests can point the subcommand at an isolated pool.
func rotateKeysOn(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, sealer *seal.Sealer) error {
	_, rotateErr := keyrotation.Rotate(ctx, pool, keyrotation.Options{Sealer: sealer, Logger: logger})
	return rotateErr
}

// runVerifyKeys checks, read-only, that every stored secret opens under the
// current master key alone. A previous key may be set but is never used.
func runVerifyKeys(ctx context.Context, logger *slog.Logger) error {
	sealer, sealErr := seal.FromEnv(secretenv.Resolve)
	if sealErr != nil {
		return sealErr
	}

	pool, _, openErr := openDatabase(ctx)
	if openErr != nil {
		return openErr
	}
	defer pool.Close()

	return verifyKeysOn(ctx, logger, pool, sealer)
}

// verifyKeysOn runs the verification against an already-open pool; see rotateKeysOn.
func verifyKeysOn(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, sealer *seal.Sealer) error {
	_, verifyErr := keyrotation.Verify(ctx, pool, sealer, logger)
	return verifyErr
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
