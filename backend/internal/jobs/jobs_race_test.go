package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// esCarreraDeArranque clasifica solo 42P01 envuelto a cualquier profundidad.
func TestEsCarreraDeArranque(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
		quiere bool
	}{
		{"nil", nil, false},
		{"error plano", errors.New("cualquier cosa"), false},
		{"pg error directo 42P01", &pgconn.PgError{Code: "42P01"}, true},
		{"pg error envuelto 42P01", fmt.Errorf("arrancando: %w", &pgconn.PgError{Code: "42P01"}), true},
		{"pg error doble envuelto 42P01", fmt.Errorf("a: %w", fmt.Errorf("b: %w", &pgconn.PgError{Code: "42P01"})), true},
		{"pg error otro código", fmt.Errorf("arrancando: %w", &pgconn.PgError{Code: "42501"}), false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := esCarreraDeArranque(c.err); got != c.quiere {
				t.Errorf("esCarreraDeArranque(%v) = %v, quiere %v", c.err, got, c.quiere)
			}
		})
	}
}

// La carrera real de #9: BD vacía → Start tropieza con 42P01 → la migración
// "de la API" llega a mitad → el reintento arranca la cola. Contra Postgres
// real, como el resto del paquete.
func TestStartConReintentosCarreraDeArranque(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no está seteado: saltando tests de integración")
	}
	// BD scratch SIN migrar: el estado del arranque en frío. El replace va
	// anclado al "?" para no tocar el "/codeowl" del userinfo de la URL.
	admin, err := pgx.Connect(context.Background(), strings.Replace(url, "/codeowl?", "/postgres?", 1))
	if err != nil {
		t.Fatalf("conectando a la BD admin: %v", err)
	}
	nombre := fmt.Sprintf("codeowl_race_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+nombre); err != nil {
		t.Fatalf("creando BD scratch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+nombre)
		_ = admin.Close(context.Background())
	})
	scratch := strings.Replace(url, "/codeowl?", "/"+nombre+"?", 1)

	st, err := store.Open(context.Background(), scratch)
	if err != nil {
		t.Fatalf("Open scratch: %v", err)
	}
	t.Cleanup(st.Close)
	q, err := New(context.Background(), st.Pool)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Sin workers Start es insert-only (no-op): registramos uno para que el
	// cliente de procesamiento toque river_job y exile el 42P01.
	if err := q.Register(&CleanupJobWorker{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// La "API" aplica la migración 700ms después del primer intento.
	go func() {
		time.Sleep(700 * time.Millisecond)
		if err := store.Migrate(scratch, migrations.FS); err != nil {
			t.Errorf("migrando scratch: %v", err)
		}
	}()

	ctx := context.Background()
	inicio := time.Now()
	// Intervalo corto de prueba: la ventana de producción (60s/2s) son consts
	// del wrapper público; acá ejercitamos el ciclo con timings de test.
	if err := q.startConReintentos(ctx, 10*time.Second, 300*time.Millisecond); err != nil {
		t.Fatalf("startConReintentos debía superar la carrera: %v", err)
	}
	elapsed := time.Since(inicio)
	if elapsed < 500*time.Millisecond {
		t.Errorf("arrancó en %v sin reintentar — la carrera no se ejercitó", elapsed)
	}
	if err := q.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
