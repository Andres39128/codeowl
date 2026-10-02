// Package migrations embebe el SQL versionado de backend/migrations para
// aplicarlo con golang-migrate (guía §3.3: forward-only, nombre descriptivo).
package migrations

import "embed"

// FS contiene las migraciones forward-only (archivos NNNN_descripcion.sql).
//
//go:embed *.sql
var FS embed.FS
