package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setStage1 deja el mínimo de etapa 1 válido y limpia el resto, para que el
// entorno del proceso no contamine los casos.
func setStage1(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"API_ADDR", "DATABASE_URL", "MASTER_KEY", "MASTER_KEY_FILE",
		"SESSION_TTL", "PASSWORD_MIN_LENGTH",
		"ADMIN_USERNAME", "ADMIN_PASSWORD", "ADMIN_PASSWORD_FILE",
		"GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY", "GITHUB_APP_PRIVATE_KEY_FILE",
		"GITHUB_WEBHOOK_SECRET", "GITHUB_WEBHOOK_SECRET_FILE",
		"WORKDIR_DISK_BUDGET", "DIFF_MAX_LINES",
		"MASTER_KEY_PREVIOUS", "MASTER_KEY_PREVIOUS_FILE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://codeowl:pw@localhost:15432/codeowl")
	t.Setenv("MASTER_KEY", base64Key())
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "una-clave-larga-suficiente")
}

func base64Key() string {
	// Base64 de exactamente 32 bytes, válida como clave AES-256.
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
}

func TestLoadFallaListandoFaltantes(t *testing.T) {
	setStage1(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("MASTER_KEY", "")
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "")

	_, err := Load()
	if err == nil {
		t.Fatal("esperaba error por variables faltantes")
	}
	for _, nombre := range []string{"DATABASE_URL", "MASTER_KEY", "ADMIN_USERNAME", "ADMIN_PASSWORD"} {
		if !strings.Contains(err.Error(), nombre) {
			t.Errorf("el error no menciona %s: %v", nombre, err)
		}
	}
}

func TestLoadVarianteFile(t *testing.T) {
	setStage1(t)
	dir := t.TempDir()

	keyFile := filepath.Join(dir, "master.key")
	// Contenido con salto de línea final: se recorta.
	if err := os.WriteFile(keyFile, []byte(base64Key()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MASTER_KEY", "")
	t.Setenv("MASTER_KEY_FILE", keyFile)

	passFile := filepath.Join(dir, "admin.pass")
	if err := os.WriteFile(passFile, []byte("clave-desde-archivo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("ADMIN_PASSWORD_FILE", passFile)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() con variantes _FILE: %v", err)
	}
	want, _ := base64.StdEncoding.DecodeString(base64Key())
	if !bytes.Equal(cfg.MasterKey, want) {
		t.Errorf("MASTER_KEY_FILE no se leyó/recortó bien: %q", cfg.MasterKey)
	}
	if cfg.AdminPassword != "clave-desde-archivo" {
		t.Errorf("ADMIN_PASSWORD_FILE no se leyó/recortó bien: %q", cfg.AdminPassword)
	}
}

func TestLoadFileGanaSobreValorDirecto(t *testing.T) {
	setStage1(t)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	if err := os.WriteFile(keyFile, []byte(base64Key()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MASTER_KEY", "no-es-la-clave")
	t.Setenv("MASTER_KEY_FILE", keyFile)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := base64.StdEncoding.DecodeString(base64Key())
	if !bytes.Equal(cfg.MasterKey, want) {
		t.Error("la variante _FILE debe ganar sobre el valor directo (§9.2)")
	}
}

func TestLoadFILEVacioEquivaleANoSeteado(t *testing.T) {
	setStage1(t)
	t.Setenv("MASTER_KEY_FILE", "")
	// MASTER_KEY directo sigue seteado por setStage1: debe cargar sin error.
	if _, err := Load(); err != nil {
		t.Fatalf("_FILE vacío no debe romper la carga: %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	setStage1(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIAddr != DefaultAPIAddr {
		t.Errorf("API_ADDR default: got %q want %q", cfg.APIAddr, DefaultAPIAddr)
	}
	if cfg.SessionTTL != 24*time.Hour {
		t.Errorf("SESSION_TTL default: got %v want 24h", cfg.SessionTTL)
	}
	if cfg.PasswordMinLength != 12 {
		t.Errorf("PASSWORD_MIN_LENGTH default: got %d want 12", cfg.PasswordMinLength)
	}
	if cfg.Stage2.DiffMaxLines != 4000 {
		t.Errorf("DIFF_MAX_LINES default: got %d want 4000", cfg.Stage2.DiffMaxLines)
	}
	if cfg.Stage2.WorkdirDiskBudget != 2<<30 {
		t.Errorf("WORKDIR_DISK_BUDGET default: got %d want 2GiB", cfg.Stage2.WorkdirDiskBudget)
	}
}

func TestLoadMasterKeyInvalida(t *testing.T) {
	setStage1(t)
	t.Setenv("MASTER_KEY", "REEMPLAZAR:openssl-rand-base64-32")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MASTER_KEY") {
		t.Errorf("MASTER_KEY no-base64 debe fallar mencionando el nombre: %v", err)
	}

	setStage1(t)
	t.Setenv("MASTER_KEY", "c2hvcnQ=") // base64 válido pero corto
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Errorf("MASTER_KEY corta debe fallar exigiendo 32 bytes: %v", err)
	}
}

func TestLoadEtapa2AusenteEsValida(t *testing.T) {
	setStage1(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("F0 corre sin credenciales VCS: %v", err)
	}
	if err := cfg.Stage2.ValidateGitHub(); err == nil {
		t.Error("ValidateGitHub debe fallar sin credenciales")
	} else {
		for _, nombre := range []string{"GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY", "GITHUB_WEBHOOK_SECRET"} {
			if !strings.Contains(err.Error(), nombre) {
				t.Errorf("ValidateGitHub no menciona %s: %v", nombre, err)
			}
		}
	}
}

func TestLoadEtapa2CompletaValida(t *testing.T) {
	setStage1(t)
	t.Setenv("GITHUB_APP_ID", "123456")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----\n...")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "secreto")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Stage2.ValidateGitHub(); err != nil {
		t.Errorf("credenciales completas deben validar: %v", err)
	}
}

func TestLoadEtapa2InvalidaFallaElArranque(t *testing.T) {
	setStage1(t)
	t.Setenv("DIFF_MAX_LINES", "abc")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DIFF_MAX_LINES") {
		t.Errorf("DIFF_MAX_LINES inválido debe fallar mencionando el nombre: %v", err)
	}

	setStage1(t)
	t.Setenv("WORKDIR_DISK_BUDGET", "12Q")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WORKDIR_DISK_BUDGET") {
		t.Errorf("WORKDIR_DISK_BUDGET inválido debe fallar mencionando el nombre: %v", err)
	}

	setStage1(t)
	t.Setenv("SESSION_TTL", "5x")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SESSION_TTL") {
		t.Errorf("SESSION_TTL inválido debe fallar mencionando el nombre: %v", err)
	}
}

func TestParseDiskSize(t *testing.T) {
	setStage1(t)
	cases := []struct {
		raw  string
		want int64
	}{
		{"2GiB", 2 << 30},
		{"512MiB", 512 << 20},
		{"1.5GiB", 1610612736},
		{"2048KiB", 2 << 20},
		{"4096", 4096}, // bytes planos
	}
	for _, c := range cases {
		t.Setenv("WORKDIR_DISK_BUDGET", c.raw)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("WORKDIR_DISK_BUDGET=%s: %v", c.raw, err)
		}
		if cfg.Stage2.WorkdirDiskBudget != c.want {
			t.Errorf("WORKDIR_DISK_BUDGET=%s: got %d want %d", c.raw, cfg.Stage2.WorkdirDiskBudget, c.want)
		}
	}
}
