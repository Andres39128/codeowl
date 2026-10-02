package store

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strconv"

	pgxmigrate "github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	migpgx5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Migrate aplica las migraciones forward-only de migrationsFS sobre
// databaseURL con golang-migrate (guía §3.3). Es idempotente: si ya están
// aplicadas no hace nada. Sin context: el engine de golang-migrate no es
// ctx-aware; su locking por advisory lock evita ejecuciones concurrentes.
func Migrate(databaseURL string, migrationsFS fs.FS) error {
	// Protocolo simple: cada migración corre como un solo Exec multi-statement,
	// necesario para los bloques $$ del schema de River (el modo extendido de
	// pgx rechaza múltiples comandos por Exec).
	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parseando DATABASE_URL: %w", err)
	}
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db := stdlib.OpenDB(*connConfig)
	defer func() { _ = db.Close() }()

	src, err := newForwardSource(migrationsFS)
	if err != nil {
		return fmt.Errorf("leyendo migraciones: %w", err)
	}
	drv, err := migpgx5.WithInstance(db, &migpgx5.Config{MultiStatementEnabled: false})
	if err != nil {
		return fmt.Errorf("preparando driver de migraciones: %w", err)
	}
	m, err := pgxmigrate.NewWithInstance("forward", src, "pgx5", drv)
	if err != nil {
		return fmt.Errorf("inicializando migraciones: %w", err)
	}
	if err := m.Up(); err != nil && err != pgxmigrate.ErrNoChange {
		return fmt.Errorf("aplicando migraciones: %w", err)
	}
	return nil
}

// forwardSource adapta golang-migrate al naming forward-only de la guía §3.3:
// archivos NNNN_descripcion.sql sin sufijo .up/.down — jamás hay down.
// (El parser default de golang-migrate exige 123_name.up.ext, que contradice
// la guía; el driver de BD sigue siendo el oficial de golang-migrate.)
type forwardSource struct {
	migrations *source.Migrations
	fsys       fs.FS
}

var forwardRegex = regexp.MustCompile(`^([0-9]+)_(.+)\.sql$`)

func newForwardSource(fsys fs.FS) (*forwardSource, error) {
	s := &forwardSource{fsys: fsys, migrations: source.NewMigrations()}
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	seen := make(map[uint]string)
	for _, name := range entries {
		m := forwardRegex.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("nombre de migración inválido (se espera NNNN_descripcion.sql): %s", name)
		}
		version, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("versión de migración inválida: %s", name)
		}
		if prev, dup := seen[uint(version)]; dup {
			return nil, fmt.Errorf("versión de migración duplicada %d: %s y %s", version, prev, name)
		}
		seen[uint(version)] = name
		if !s.migrations.Append(&source.Migration{
			Version:    uint(version),
			Identifier: m[2],
			Direction:  source.Up,
			Raw:        name,
		}) {
			return nil, fmt.Errorf("migración duplicada: %s", name)
		}
	}
	return s, nil
}

func (s *forwardSource) Open(string) (source.Driver, error) {
	return nil, fmt.Errorf("forwardSource no se abre por URL: usar newForwardSource")
}

func (s *forwardSource) Close() error { return nil }

func (s *forwardSource) First() (uint, error) {
	if v, ok := s.migrations.First(); ok {
		return v, nil
	}
	return 0, os.ErrNotExist
}

func (s *forwardSource) Prev(version uint) (uint, error) {
	if v, ok := s.migrations.Prev(version); ok {
		return v, nil
	}
	return 0, os.ErrNotExist
}

func (s *forwardSource) Next(version uint) (uint, error) {
	if v, ok := s.migrations.Next(version); ok {
		return v, nil
	}
	return 0, os.ErrNotExist
}

func (s *forwardSource) ReadUp(version uint) (io.ReadCloser, string, error) {
	m, ok := s.migrations.Up(version)
	if !ok {
		return nil, "", os.ErrNotExist
	}
	f, err := s.fsys.Open(m.Raw)
	if err != nil {
		return nil, "", err
	}
	return f, m.Identifier, nil
}

// ReadDown nunca devuelve migraciones: solo adelante (guía §3.3).
func (s *forwardSource) ReadDown(uint) (io.ReadCloser, string, error) {
	return nil, "", os.ErrNotExist
}

// La interfaz source.Driver exige Open, Close, First, Prev, Next, ReadUp y
// ReadDown; el cast documenta en compilación que forwardSource la cumple.
var _ source.Driver = (*forwardSource)(nil)
var _ database.Driver = (*migpgx5.Postgres)(nil)
