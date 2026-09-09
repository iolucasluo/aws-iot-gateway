package seeds

import "embed"

// Files contains the explicit runtime contract seeds and non-production product seeds.
//
//go:embed *.sql
var Files embed.FS
