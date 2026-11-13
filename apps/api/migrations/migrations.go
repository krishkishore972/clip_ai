// Package migrations embeds the versioned SQL files for the migration command.
package migrations

import "embed"

// Files contains the SQL migrations shipped with this build.
//
//go:embed *.sql
var Files embed.FS
