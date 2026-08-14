package migrations

import "embed"

// Files contains the ordered PostgreSQL schema migrations shipped with the API
// and worker binaries.
//
//go:embed sql/*.sql
var Files embed.FS
