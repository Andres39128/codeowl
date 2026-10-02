package vcs

// Tests del mapeo finding → posición (§3.6.5): hunks reales de diff
// unificado, contexto colapsado incluido. Sin BD: fixtures en memoria.

import "testing"

// diffFixture es un diff unificado con dos archivos y tres hunks: líneas
// nuevas (RIGHT), eliminadas, contexto y un hunk de conteos colapsados
// ("@@ -1 +1 @@", el formato de git para cambios de 1 línea).
const diffFixture = `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,6 @@ package main
 context line 1
 context line 2
-removed line 3
+added line 3
+added line 4
 context line 5
@@ -20,4 +21,4 @@ func main() {
 context line 20
-removed line 21
+replaced line 21
 context line 22
 context line 23
diff --git a/util.py b/util.py
index 333..444 100644
--- a/util.py
+++ b/util.py
@@ -1 +1 @@
-old one-liner
+new one-liner
`

func TestMapFindingToPosition(t *testing.T) {
	cases := []struct {
		name string
		file string
		line int32
		want CommentPosition
		ok   bool
	}{
		// Líneas nuevas: lado RIGHT.
		{"línea agregada", "main.go", 3, CommentPosition{File: "main.go", Line: 3, Side: SideRight}, true},
		{"línea agregada segunda", "main.go", 4, CommentPosition{File: "main.go", Line: 4, Side: SideRight}, true},
		// Reemplazo (-21/+21): RIGHT tiene precedencia — el finding
		// describe el código del head.
		{"reemplazo ancla a la nueva", "main.go", 21, CommentPosition{File: "main.go", Line: 21, Side: SideRight}, true},
		// Contexto: existe en ambos lados, ancla RIGHT.
		{"contexto", "main.go", 1, CommentPosition{File: "main.go", Line: 1, Side: SideRight}, true},
		{"contexto del segundo hunk", "main.go", 22, CommentPosition{File: "main.go", Line: 22, Side: SideRight}, true},
		// Hunk con conteos colapsados: la nueva 1 existe → RIGHT.
		{"one-liner colapsado", "util.py", 1, CommentPosition{File: "util.py", Line: 1, Side: SideRight}, true},
		// Fuera del diff visible: baja al resumen (§3.6.5).
		{"línea fuera del diff", "main.go", 100, CommentPosition{}, false},
		{"archivo ausente", "nope.go", 1, CommentPosition{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MapFindingToPosition(diffFixture, tc.file, tc.line)
			if ok != tc.ok {
				t.Fatalf("ok: got %v want %v (pos %+v)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("posición: got %+v want %+v", got, tc.want)
			}
		})
	}
}

// TestMapFindingToPositionEliminada verifica el caso LEFT end-to-end: el
// hallazgo sobre una línea que el PR eliminó y cuya altura ya no existe en
// el lado nuevo ancla al LEFT del hunk.
func TestMapFindingToPositionEliminada(t *testing.T) {
	// old: a(1), rem1(2), rem2(3), rem3(4), b(5) — new: a(1), b(2).
	// La altura 3 solo existe del lado viejo.
	diff := `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,2 @@ package main
 context a
-removed one
-removed two
-removed three
 context b
`
	got, ok := MapFindingToPosition(diff, "main.go", 3)
	if !ok {
		t.Fatal("la línea 3 vieja debe anclar al lado LEFT")
	}
	if got.Side != SideLeft || got.Line != 3 || got.File != "main.go" {
		t.Errorf("línea eliminada: got %+v", got)
	}

	// La altura 5 ya no existe en ningún lado: baja al resumen (§3.6.5).
	if _, ok := MapFindingToPosition(diff, "main.go", 5); ok {
		t.Error("la altura 5 no está en el diff visible: debe devolver ok=false")
	}
}
