package seeds

import "embed"

// Files contains the explicitly executed non-production seed SQL.
//
//go:embed *.sql
var Files embed.FS
