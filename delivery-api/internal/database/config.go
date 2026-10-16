// Package database holds delivery-api's Postgres connection configuration and
// pool construction.
package database

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
)

const (
	envHost     = "DELIVERY_DB_HOST"
	envPort     = "DELIVERY_DB_PORT"
	envUser     = "DELIVERY_DB_USER"
	envName     = "DELIVERY_DB_NAME"
	envPassword = "DELIVERY_DB_PASSWORD"

	defaultPort = "5432"
)

// Config is the set of values needed to reach delivery-api's Postgres.
type Config struct {
	Host     string
	Port     string
	User     string
	Name     string
	Password string
}

// ConfigFromEnv builds a Config from the DELIVERY_DB_* environment. getenv reads
// plain variables and resolveSecret resolves the password (typically from a
// DELIVERY_DB_PASSWORD_FILE indirection). Host, user, name and password are
// mandatory: a missing one is an error, never a silent default, so the service
// fails fast instead of connecting to the wrong place. Port defaults to 5432.
func ConfigFromEnv(
	getenv func(string) string,
	resolveSecret func(string) (string, error),
) (Config, error) {
	config := Config{
		Host: getenv(envHost),
		Port: getenv(envPort),
		User: getenv(envUser),
		Name: getenv(envName),
	}

	if config.Port == "" {
		config.Port = defaultPort
	}
	for _, required := range []struct{ key, value string }{
		{envHost, config.Host},
		{envUser, config.User},
		{envName, config.Name},
	} {
		if required.value == "" {
			return Config{}, fmt.Errorf("%s is required", required.key)
		}
	}

	password, resolveErr := resolveSecret(envPassword)
	if resolveErr != nil {
		return Config{}, fmt.Errorf("resolve %s: %w", envPassword, resolveErr)
	}
	if password == "" {
		return Config{}, fmt.Errorf("%s is required", envPassword)
	}
	config.Password = password

	return config, nil
}

// String renders config with the password redacted, so formatting a Config
// with %v or %+v can never leak the credential.
func (config Config) String() string {
	return fmt.Sprintf("{Host:%s Port:%s User:%s Name:%s Password:[redacted]}",
		config.Host, config.Port, config.User, config.Name)
}

// LogValue redacts the password when a Config is passed to slog.
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}

// ConnString renders config as a postgres:// URL. It is built with url.URL and
// url.UserPassword so special characters in the credentials are escaped.
// TLS is disabled because Postgres is only reachable on the private compose
// network.
func (config Config) ConnString() string {
	connURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(config.User, config.Password),
		Host:     net.JoinHostPort(config.Host, config.Port),
		Path:     "/" + config.Name,
		RawQuery: "sslmode=disable",
	}
	return connURL.String()
}
