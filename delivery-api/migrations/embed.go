// Package migrations embeds delivery-api's numbered SQL migrations so the
// binary carries its own schema and the deploy needs no extra files.
package migrations

import "embed"

// FS holds the *.sql migration files. File names must match
// ^(\d{4})_([a-z0-9_]+)\.sql$ (see internal/migrate).
//
//go:embed *.sql
var FS embed.FS
