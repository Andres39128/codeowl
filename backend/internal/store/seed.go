package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Andres39128/codeowl/backend/internal/config"
)

// SeedAdmin crea el admin inicial desde env si el username no existe
// (guía §3.4). Idempotente: si ya existe no toca nada — jamás sobreescribe la
// contraseña al reiniciar (la credencial la definió el operador y arranca con
// must_change_password en false).
func SeedAdmin(ctx context.Context, st *Store, cfg *config.Config) error {
	_, err := st.GetByUsername(ctx, cfg.AdminUsername)
	if err == nil {
		return nil // ya existe: no-op
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("buscando el admin %q: %w", cfg.AdminUsername, err)
	}

	if len(cfg.AdminPassword) < cfg.PasswordMinLength {
		return fmt.Errorf("ADMIN_PASSWORD tiene %d caracteres; el mínimo configurado es %d (PASSWORD_MIN_LENGTH)",
			len(cfg.AdminPassword), cfg.PasswordMinLength)
	}
	hash, err := HashPassword(cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("hasheando la contraseña del admin: %w", err)
	}

	_, err = st.CreateUser(ctx, CreateUserParams{
		Username:           cfg.AdminUsername,
		Role:               "admin",
		PasswordHash:       hash,
		MustChangePassword: false, // la credencial la definió el operador (§3.4)
	})
	if err != nil {
		// Carrera entre dos procesos sembrando a la vez: el UNIQUE de username
		// resuelve; quien llega segundo trata como no-op.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil
		}
		return fmt.Errorf("creando el admin %q: %w", cfg.AdminUsername, err)
	}
	return nil
}
