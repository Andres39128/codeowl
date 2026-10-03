package analyze

import (
	"context"
	"strings"
	"testing"
)

// fixture de salida del modo --symbols: dos archivos recorridos, uno roto
// (b.py con error de sintaxis → cero filas, sin error — §6 F4) y uno sano
// con dos símbolos que repiten los imports del archivo en cada fila.
const symbolsFixtureJSON = `{
  "files_analyzed": 2,
  "files": [
    {"path": "a.go", "language": "go"},
    {"path": "b.py", "language": "python"}
  ],
  "symbols": [
    {"file": "a.go", "symbol": "Hola", "kind": "function", "start_line": 3, "end_line": 4, "imports": ["fmt"]},
    {"file": "a.go", "symbol": "Saludo", "kind": "interface", "start_line": 6, "end_line": 8, "imports": ["fmt"]}
  ]
}`

func TestParseSymbolsValid(t *testing.T) {
	syms, err := ParseSymbols([]byte(symbolsFixtureJSON))
	if err != nil {
		t.Fatalf("ParseSymbols() error = %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("Symbols = %d filas, want 2", len(syms))
	}
	if syms[0].File != "a.go" || syms[0].Symbol != "Hola" || syms[0].Kind != "function" ||
		syms[0].StartLine != 3 || syms[0].EndLine != 4 {
		t.Errorf("fila 0 mal tipada: %+v", syms[0])
	}
	// imports a nivel de archivo, repetidos en cada fila del mismo.
	if len(syms[0].Imports) != 1 || syms[0].Imports[0] != "fmt" {
		t.Errorf("imports de Hola = %v, want [fmt]", syms[0].Imports)
	}
	if syms[1].Kind != "interface" {
		t.Errorf("kind de Saludo = %q, want interface", syms[1].Kind)
	}
}

// Archivo roto: en files pero sin filas en symbols — NO es error (§6 F4).
func TestParseSymbolsBrokenFileIsNotError(t *testing.T) {
	syms, err := ParseSymbols([]byte(`{
	  "files_analyzed": 2,
	  "files": [{"path": "roto.go", "language": "go"}, {"path": "ok.py", "language": "python"}],
	  "symbols": [{"file": "ok.py", "symbol": "Solo", "kind": "class", "start_line": 1, "end_line": 2}]
	}`))
	if err != nil {
		t.Fatalf("ParseSymbols() con archivo roto no debe fallar: %v", err)
	}
	if len(syms) != 1 || syms[0].File != "ok.py" {
		t.Errorf("solo ok.py debe tener filas: %+v", syms)
	}
}

func TestParseSymbolsRejectsContractViolations(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"kind fuera del conjunto cerrado", `{"files_analyzed":1,"files":[],
		  "symbols":[{"file":"a.go","symbol":"X","kind":"macro","start_line":1,"end_line":2}]}`},
		{"file vacío", `{"files_analyzed":1,"files":[],
		  "symbols":[{"file":"","symbol":"X","kind":"function","start_line":1,"end_line":2}]}`},
		{"symbol vacío", `{"files_analyzed":1,"files":[],
		  "symbols":[{"file":"a.go","symbol":"","kind":"function","start_line":1,"end_line":2}]}`},
		{"líneas invertidas", `{"files_analyzed":1,"files":[],
		  "symbols":[{"file":"a.go","symbol":"X","kind":"function","start_line":5,"end_line":2}]}`},
		{"no es JSON", `algo roto`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSymbols([]byte(tc.json)); err == nil {
				t.Fatalf("ParseSymbols() esperaba error, no hubo")
			}
		})
	}
}

// Salida sin símbolos: recorrido limpio sin declaraciones — válido.
func TestParseSymbolsEmpty(t *testing.T) {
	syms, err := ParseSymbols([]byte(`{"files_analyzed":1,"files":[{"path":"x.js","language":"js"}],"symbols":[]}`))
	if err != nil {
		t.Fatalf("ParseSymbols() error = %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("Symbols = %d, want 0", len(syms))
	}
}

// TestBuildSymbolsArgs: --symbols va al final, después del workdir — el CLI
// espera `analyzer /workdir [--symbols]`.
func TestBuildSymbolsArgs(t *testing.T) {
	args := buildSymbolsArgs("/tmp/job-1", testConfig())
	if args[len(args)-1] != "--symbols" {
		t.Errorf("último argumento = %q, want --symbols", args[len(args)-1])
	}
	if args[len(args)-2] != "/workdir" {
		t.Errorf("workdir = %q, want /workdir antes de --symbols", args[len(args)-2])
	}
	// Los flags de §9.4 se mantienen: mismo sandbox, modo distinto.
	joined := strings.Join(args, " ")
	for _, want := range []string{"--network=none", "--read-only"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSymbolsArgs() sin %q: %v", want, args)
		}
	}
}

// Con imagen: ExtractSymbols sobre el fixture del repo (requiere la imagen
// con el modo --symbols: just analyzer-build). main.go del fixture aporta
// una función y una interfaz — kinds del conjunto cerrado.
func TestExtractSymbolsAgainstFixture(t *testing.T) {
	imageAvailable(t)
	runner := NewRunner(testConfig())
	syms, err := runner.ExtractSymbols(context.Background(), fixtureRepo(t))
	if err != nil {
		t.Fatalf("ExtractSymbols() error = %v", err)
	}
	byName := map[string]Symbol{}
	for _, s := range syms {
		byName[s.Symbol] = s
	}
	hola, ok := byName["Hola"]
	if !ok || hola.Kind != "function" || hola.File != "main.go" || hola.StartLine != 3 || hola.EndLine != 3 {
		t.Errorf("Hola mal extraída: %+v (presente=%v)", hola, ok)
	}
	saludo, ok := byName["Saludo"]
	if !ok || saludo.Kind != "interface" || saludo.File != "main.go" {
		t.Errorf("Saludo mal extraída: %+v (presente=%v)", saludo, ok)
	}
	// main.go no importa nada: imports file-level vacíos en cada fila.
	if len(hola.Imports) != 0 {
		t.Errorf("imports de Hola = %v, want vacío", hola.Imports)
	}
	// Un archivo sin declaraciones no produce filas: go.mod no es código,
	// .env y src/app.js no declaran símbolos en el fixture.
	if _, ok := byName["unused"]; ok {
		t.Errorf("variable sin declaración no debe ser símbolo: %+v", syms)
	}
}
