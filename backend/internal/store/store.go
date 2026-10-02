// Package store es el único lugar con SQL del sistema (mapa: backend.store):
// queries sqlc generadas, modelos de dominio y migraciones aplicadas.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store envuelve el pool de conexiones y expone las queries sqlc generadas.
type Store struct {
	Pool *pgxpool.Pool
	*Queries
}

// Open abre el pool de conexiones contra databaseURL. Se llama Open (y no New)
// porque sqlc genera New(db DBTX) en este paquete.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("conectando a la base de datos: %w", err)
	}
	return &Store{Pool: pool, Queries: New(pool)}, nil
}

// Ping verifica la conectividad con la base de datos.
func (s *Store) Ping(ctx context.Context) error {
	return s.Pool.Ping(ctx)
}

// Close cierra el pool de conexiones.
func (s *Store) Close() {
	s.Pool.Close()
}
