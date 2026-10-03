package store

import (
	"errors"
	"io"
	"os"
	"testing"
	"testing/fstest"

	"github.com/Andres39128/codeowl/backend/migrations"
)

func TestForwardSourceParseaMigracionesReales(t *testing.T) {
	src, err := newForwardSource(migrations.FS)
	if err != nil {
		t.Fatalf("el FS real de migraciones debe parsear: %v", err)
	}
	first, err := src.First()
	if err != nil {
		t.Fatalf("First(): %v", err)
	}
	if first != 1 {
		t.Errorf("primera versión: got %d want 1", first)
	}
	second, err := src.Next(first)
	if err != nil {
		t.Fatalf("Next(1): %v", err)
	}
	if second != 2 {
		t.Errorf("segunda versión: got %d want 2", second)
	}
	third, err := src.Next(second)
	if err != nil {
		t.Fatalf("Next(2): %v", err)
	}
	if third != 3 {
		t.Errorf("tercera versión: got %d want 3", third)
	}
	fourth, err := src.Next(third)
	if err != nil {
		t.Fatalf("Next(3): %v", err)
	}
	if fourth != 4 {
		t.Errorf("cuarta versión: got %d want 4", fourth)
	}
	fifth, err := src.Next(fourth)
	if err != nil {
		t.Fatalf("Next(4): %v", err)
	}
	if fifth != 5 {
		t.Errorf("quinta versión: got %d want 5", fifth)
	}
	if _, err := src.Next(fifth); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no hay sexta versión: esperaba os.ErrNotExist, got %v", err)
	}

	rc, id, err := src.ReadUp(1)
	if err != nil {
		t.Fatalf("ReadUp(1): %v", err)
	}
	defer func() { _ = rc.Close() }()
	if id == "" {
		t.Error("ReadUp debe devolver el identificador")
	}
	body, err := io.ReadAll(rc)
	if err != nil || len(body) == 0 {
		t.Errorf("ReadUp(1) debe devolver el SQL: len=%d err=%v", len(body), err)
	}
}

func TestForwardSourceReadDownEsErrNotExist(t *testing.T) {
	// Solo adelante (guía §3.3): nunca hay down.
	src, err := newForwardSource(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.ReadDown(1); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadDown debe ser siempre os.ErrNotExist, got %v", err)
	}
}

func TestForwardSourceRechazaDuplicadosYNombresInvalidos(t *testing.T) {
	dup := fstest.MapFS{
		"0001_a.sql": {Data: []byte("SELECT 1;")},
		"0001_b.sql": {Data: []byte("SELECT 2;")},
	}
	if _, err := newForwardSource(dup); err == nil {
		t.Error("versiones duplicadas deben fallar")
	}

	badName := fstest.MapFS{
		"migracion_sin_version.sql": {Data: []byte("SELECT 1;")},
	}
	if _, err := newForwardSource(badName); err == nil {
		t.Error("nombres sin NNNN_ deben fallar")
	}
}
