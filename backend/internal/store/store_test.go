package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// Los tests de integración corren contra el Postgres de desarrollo: requieren
// DATABASE_URL en el entorno (just test lo exporta desde el .env de la raíz).
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no está seteado: saltando tests de integración")
	}
	st, err := Open(context.Background(), url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	// Aplica migraciones si faltan (idempotente, mismo camino del arranque de
	// la api): los tests de seed/sesiones requieren el schema.
	if err := Migrate(url, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return st
}

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		AdminUsername:     "admin-test-" + fmt.Sprint(time.Now().UnixNano()),
		AdminPassword:     "clave-larga-de-test-123",
		PasswordMinLength: 12,
	}
}

// testDatabase crea una BD efímera para probar migraciones sobre esquema
// vacío y la borra al terminar.
func testDatabase(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no está seteado: saltando tests de integración")
	}
	st := testStore(t)
	name := fmt.Sprintf("codeowl_migrate_test_%d", time.Now().UnixNano())
	if _, err := st.Pool.Exec(context.Background(), fmt.Sprintf("CREATE DATABASE %s", name)); err != nil {
		t.Fatalf("creando BD efímera: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(), fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name))
	})
	// Reemplaza el nombre de BD en la URL (último segmento del path).
	base := url[:strings.LastIndex(url, "/")+1]
	params := ""
	if i := strings.Index(url, "?"); i >= 0 {
		params = url[i:]
	}
	return base + name + params
}

func TestMigrateSobreBDEmpiezaLimpiaYEsIdempotente(t *testing.T) {
	freshURL := testDatabase(t)

	if err := Migrate(freshURL, migrations.FS); err != nil {
		t.Fatalf("primera corrida sobre BD vacía: %v", err)
	}
	// Segunda corrida: golang-migrate verifica la versión y no hace nada.
	if err := Migrate(freshURL, migrations.FS); err != nil {
		t.Fatalf("segunda corrida (idempotencia): %v", err)
	}

	st, err := Open(context.Background(), freshURL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Tablas núcleo: F0 (users/sessions/River) + F1 (guía §3.3, migración 0003)
	// + F4 (repo_index con pgvector, migración 0004).
	for _, tabla := range []string{
		"users", "sessions", "river_job", "river_queue", "river_migration", "schema_migrations",
		"llm_providers", "repositories", "pull_requests", "reviews",
		"findings", "comments_sent", "webhook_deliveries", "llm_usage", "repo_index",
	} {
		var n int
		if err := st.Pool.QueryRow(context.Background(),
			"SELECT COUNT(1) FROM information_schema.tables WHERE table_name = $1", tabla).Scan(&n); err != nil || n != 1 {
			t.Errorf("la tabla %s debe existir tras migrar (n=%d err=%v)", tabla, n, err)
		}
	}
}

func TestSeedAdminIdempotente(t *testing.T) {
	st := testStore(t)
	cfg := testCfg(t)
	ctx := context.Background()

	if err := SeedAdmin(ctx, st, cfg); err != nil {
		t.Fatalf("primer SeedAdmin: %v", err)
	}
	user, err := st.GetByUsername(ctx, cfg.AdminUsername)
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if user.Role != "admin" || user.MustChangePassword {
		t.Errorf("el admin debe nacer role=admin y must_change_password=false: %+v", user)
	}

	// Segunda corrida: no-op — jamás sobreescribe la contraseña (§3.4).
	if err := SeedAdmin(ctx, st, cfg); err != nil {
		t.Fatalf("segundo SeedAdmin: %v", err)
	}
	after, err := st.GetByUsername(ctx, cfg.AdminUsername)
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash != user.PasswordHash || after.ID != user.ID {
		t.Error("el segundo SeedAdmin no debe tocar el usuario existente")
	}
}

func TestSeedAdminRechazaPasswordCorta(t *testing.T) {
	st := testStore(t)
	cfg := testCfg(t)
	cfg.AdminPassword = "corta"
	if err := SeedAdmin(context.Background(), st, cfg); err == nil {
		t.Error("una contraseña bajo PASSWORD_MIN_LENGTH debe fallar")
	}
}

func TestSesionHashRoundTrip(t *testing.T) {
	st := testStore(t)
	cfg := testCfg(t)
	ctx := context.Background()

	if err := SeedAdmin(ctx, st, cfg); err != nil {
		t.Fatal(err)
	}
	user, err := st.GetByUsername(ctx, cfg.AdminUsername)
	if err != nil {
		t.Fatal(err)
	}

	token, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := HashSessionToken(token)

	// El token plano jamás llega a la BD: solo su hash.
	created, err := st.CreateSession(ctx, CreateSessionParams{
		TokenHash: hash,
		UserID:    user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	row, err := st.GetSessionByTokenHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetSessionByTokenHash: %v", err)
	}
	if row.ID != created.ID || row.Username != user.Username || row.Role != "admin" {
		t.Errorf("round-trip de sesión: got %+v", row)
	}

	// Una sesión expirada no es recuperable por su hash.
	expired, err := st.CreateSession(ctx, CreateSessionParams{
		TokenHash: HashSessionToken(token + "-expirada"),
		UserID:    user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSessionByTokenHash(ctx, expired.TokenHash); err == nil {
		t.Error("una sesión expirada no debe resolverse")
	}

	// Logout revoca la sesión por hash.
	if err := st.DeleteSession(ctx, hash); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.GetSessionByTokenHash(ctx, hash); err == nil {
		t.Error("la sesión borrada no debe resolverse")
	}

	// Reset de contraseña / baja: revoca todas las sesiones del usuario (§3.4).
	if _, err := st.CreateSession(ctx, CreateSessionParams{
		TokenHash: HashSessionToken(token + "-otra"),
		UserID:    user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSessionsForUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteSessionsForUser: %v", err)
	}
	if _, err := st.GetSessionByTokenHash(ctx, HashSessionToken(token+"-otra")); err == nil {
		t.Error("DeleteSessionsForUser debe revocar todas las sesiones del usuario")
	}
}

func TestPasswordHashFormato(t *testing.T) {
	const pass = "una-clave-larga-suficiente"
	hash, err := HashPassword(pass)
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) < 90 || hash[:10] != "$argon2id$" {
		t.Errorf("el hash debe ser PHC argon2id, got %q", hash)
	}
	otro, _ := HashPassword(pass)
	if hash == otro {
		t.Error("dos hashes de la misma contraseña no deben coincidir (salt aleatorio)")
	}
}

func TestCheckPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("clave-correcta-123")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "clave-correcta-123") {
		t.Error("CheckPassword debe aceptar la contraseña correcta")
	}
	if CheckPassword(hash, "clave-incorrecta") {
		t.Error("CheckPassword debe rechazar una contraseña distinta")
	}
	if CheckPassword("no-es-formato-phc", "x") {
		t.Error("CheckPassword debe rechazar hashes malformados")
	}
}

func TestSessionTokenEntropia(t *testing.T) {
	a, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("dos tokens no deben coincidir")
	}
	if len(a) != 43 { // 256 bits en base64 URL-safe sin padding
		t.Errorf("el token debe tener 43 caracteres (256 bits), tiene %d", len(a))
	}
}
