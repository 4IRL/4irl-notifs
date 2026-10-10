// Package migrate loads numbered SQL migrations and applies the pending ones to
// Postgres, recording failures so the readiness endpoint can surface them.
package migrate

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// migrationFileName matches NNNN_snake_case.sql.
var migrationFileName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// sqlSuffix marks a file as a migration candidate; anything else is ignored.
const sqlSuffix = ".sql"

// Migration is one numbered SQL migration.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Load reads every *.sql file in the root of fsys as a migration and returns
// them sorted by version ascending. Non-.sql files are ignored; a .sql file
// whose name does not match ^(\d{4})_([a-z0-9_]+)\.sql$, or a repeated
// version, is an error. An empty filesystem yields an empty (non-nil) slice.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, readErr := fs.ReadDir(fsys, ".")
	if readErr != nil {
		return nil, fmt.Errorf("read migrations: %w", readErr)
	}

	migrations := []Migration{}
	seenVersions := map[int]string{}
	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(fileName, sqlSuffix) {
			continue
		}

		matches := migrationFileName.FindStringSubmatch(fileName)
		if matches == nil {
			return nil, fmt.Errorf("migration %q: name must match %s", fileName, migrationFileName)
		}
		version, parseErr := strconv.Atoi(matches[1])
		if parseErr != nil {
			return nil, fmt.Errorf("migration %q: parse version: %w", fileName, parseErr)
		}
		if previous, duplicate := seenVersions[version]; duplicate {
			return nil, fmt.Errorf("migration %q: version %d already used by %q", fileName, version, previous)
		}
		seenVersions[version] = fileName

		contents, fileErr := fs.ReadFile(fsys, fileName)
		if fileErr != nil {
			return nil, fmt.Errorf("read migration %q: %w", fileName, fileErr)
		}
		migrations = append(migrations, Migration{Version: version, Name: matches[2], SQL: string(contents)})
	}

	sort.Slice(migrations, func(left int, right int) bool {
		return migrations[left].Version < migrations[right].Version
	})
	return migrations, nil
}

// ExpectedVersion returns the highest version in migrations, or 0 when empty.
func ExpectedVersion(migrations []Migration) int {
	highest := 0
	for _, migration := range migrations {
		if migration.Version > highest {
			highest = migration.Version
		}
	}
	return highest
}
