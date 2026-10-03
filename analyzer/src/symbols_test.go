// Tests del modo --symbols (§6 F4): extracción tree-sitter sobre el fixture
// analyzer/src/test (mapa analyzer.cli.pruebas). Corren las funciones de
// extracción directamente — nunca spawnean el binario; el end-to-end se
// verifica con el smoke manual del CLI (README del fixture).
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestExtractSymbolsFixtures verifica símbolos exactos (nombre, kind, líneas
// 1-based inclusivas e imports a nivel de archivo) para cada fixture.
func TestExtractSymbolsFixtures(t *testing.T) {
	goImports := []string{"fmt", "os/exec"}
	jsImports := []string{"./saludos.js", "node:path"}
	cases := []struct {
		name string
		file string
		lang string
		want []symbolRow
	}{
		{
			name: "go: type+interface+funcion+metodo",
			file: "main.go",
			lang: "go",
			want: []symbolRow{
				{Symbol: "Greeter", Kind: "type", StartLine: 9, EndLine: 11, Imports: goImports},
				{Symbol: "Speaker", Kind: "interface", StartLine: 14, EndLine: 16, Imports: goImports},
				{Symbol: "Greet", Kind: "function", StartLine: 19, EndLine: 21, Imports: goImports},
				{Symbol: "Speak", Kind: "method", StartLine: 24, EndLine: 26, Imports: goImports},
			},
		},
		{
			name: "js: funcion+clase+metodo",
			file: "app.js",
			lang: "js",
			want: []symbolRow{
				{Symbol: "chau", Kind: "function", StartLine: 5, EndLine: 7, Imports: jsImports},
				{Symbol: "Greeter", Kind: "class", StartLine: 9, EndLine: 13, Imports: jsImports},
				{Symbol: "greet", Kind: "method", StartLine: 10, EndLine: 12, Imports: jsImports},
			},
		},
		{
			name: "ts: grammar de javascript (limitación documentada)",
			file: "app.ts",
			lang: "js",
			want: []symbolRow{
				{Symbol: "chau", Kind: "function", StartLine: 5, EndLine: 7, Imports: []string{"./saludos.js"}},
			},
		},
		{
			name: "python: funcion+clase+metodo",
			file: "app.py",
			lang: "python",
			want: []symbolRow{
				{Symbol: "hola", Kind: "function", StartLine: 6, EndLine: 7, Imports: []string{"os", "collections"}},
				{Symbol: "Greeter", Kind: "class", StartLine: 10, EndLine: 12, Imports: []string{"os", "collections"}},
				{Symbol: "greet", Kind: "method", StartLine: 11, EndLine: 12, Imports: []string{"os", "collections"}},
			},
		},
		{
			name: "rust: struct+trait+fn (fn de trait=function, fn de impl=method)",
			file: "lib.rs",
			lang: "rust",
			want: []symbolRow{
				{Symbol: "Greeter", Kind: "type", StartLine: 5, EndLine: 8, Imports: []string{"std::collections::HashMap", "crate::saludos::hola"}},
				{Symbol: "Speaker", Kind: "trait", StartLine: 10, EndLine: 12, Imports: []string{"std::collections::HashMap", "crate::saludos::hola"}},
				{Symbol: "speak", Kind: "function", StartLine: 11, EndLine: 11, Imports: []string{"std::collections::HashMap", "crate::saludos::hola"}},
				{Symbol: "speak", Kind: "method", StartLine: 15, EndLine: 17, Imports: []string{"std::collections::HashMap", "crate::saludos::hola"}},
				{Symbol: "hola", Kind: "function", StartLine: 20, EndLine: 22, Imports: []string{"std::collections::HashMap", "crate::saludos::hola"}},
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("test", "testdata", tt.file))
			if err != nil {
				t.Fatalf("leyendo fixture: %v", err)
			}
			got, err := extractSymbols(tt.lang, src)
			if err != nil {
				t.Fatalf("extractSymbols(%s): %v", tt.file, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("extractSymbols(%s) devolvió %d símbolos %+v, want %d", tt.file, len(got), got, len(tt.want))
			}
			for i, w := range tt.want {
				g := got[i]
				// File lo completa emitSymbolReport: la extracción es por contenido.
				g.File, w.File = "", ""
				if !reflect.DeepEqual(g, w) {
					t.Errorf("símbolo %d de %s:\n  got  %+v\n  want %+v", i, tt.file, g, w)
				}
			}
		})
	}
}

// TestExtractSymbolsSyntaxError: un archivo roto devuelve error (0 símbolos)
// y emitSymbolReport lo tolera — el run sigue (§6 F4).
func TestExtractSymbolsSyntaxError(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("test", "testdata", "broken.go"))
	if err != nil {
		t.Fatalf("leyendo fixture: %v", err)
	}
	rows, err := extractSymbols("go", src)
	if err == nil {
		t.Fatalf("broken.go parseó sin error: %+v", rows)
	}
	if len(rows) != 0 {
		t.Errorf("broken.go con símbolos: %+v", rows)
	}
}

// TestEmitSymbolReport: contrato completo sobre el fixture repo — README no
// se analiza, broken.go sale con cero símbolos y motivo en stderr, imports
// repetidos por fila, JSON válido por stdout.
func TestEmitSymbolReport(t *testing.T) {
	files, err := walk("test")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	// README.md jamás entra: la detección es por extensión de código.
	if got := strings.Join(paths, ","); got != "testdata/app.js,testdata/app.py,testdata/app.ts,testdata/broken.go,testdata/lib.rs,testdata/main.go" {
		t.Fatalf("walk devolvió %q", got)
	}

	var stdout, stderr bytes.Buffer
	if err := emitSymbolReport("test", files, &stderr, &stdout); err != nil {
		t.Fatalf("emitSymbolReport: %v", err)
	}
	if !strings.Contains(stderr.String(), "testdata/broken.go") {
		t.Errorf("stderr sin motivo de broken.go: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "README") {
		t.Errorf("stderr menciona README: %q", stderr.String())
	}

	var rep symbolReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("stdout no es JSON válido: %v — %q", err, stdout.String())
	}
	if rep.FilesAnalyzed != 6 {
		t.Errorf("files_analyzed = %d, want 6", rep.FilesAnalyzed)
	}
	if len(rep.Files) != 6 {
		t.Errorf("files = %d entradas, want 6", len(rep.Files))
	}
	perFile := map[string]int{}
	seenImports := map[string][]string{}
	for _, s := range rep.Symbols {
		if s.File == "testdata/broken.go" {
			t.Errorf("broken.go aportó símbolos: %+v", s)
		}
		perFile[s.File]++
		if seenImports[s.File] == nil {
			seenImports[s.File] = s.Imports
		} else if !reflect.DeepEqual(seenImports[s.File], s.Imports) {
			t.Errorf("imports de %s inconsistentes entre filas: %v vs %v", s.File, seenImports[s.File], s.Imports)
		}
	}
	wantPerFile := map[string]int{
		"testdata/main.go": 4, "testdata/app.js": 3, "testdata/app.py": 3,
		"testdata/lib.rs": 5, "testdata/app.ts": 1,
	}
	if !reflect.DeepEqual(perFile, wantPerFile) {
		t.Errorf("símbolos por archivo = %v, want %v", perFile, wantPerFile)
	}
}

// TestExtractImportsEdge: bordes de los regex best-effort que los fixtures
// no cubren (alias, multi-import, use-trees, side-effect import).
func TestExtractImportsEdge(t *testing.T) {
	cases := []struct {
		name string
		lang string
		src  string
		want []string
	}{
		{"go alias y blank", "go", "package a\n\nimport _ \"embed\"\nimport m \"math/rand\"\n", []string{"embed", "math/rand"}},
		{"go multi en bloque", "go", "import (\n\t\"a\"\n\tb \"b/c\"\n)\n", []string{"a", "b/c"}},
		{"python alias multi", "python", "import a as b, c.d\nfrom .rel import x\n", []string{"a", "c.d", ".rel"}},
		{"js side-effect", "js", "import './polyfill.js';\nimport def, {x} from \"pkg\";\nconst fs = require('fs');\n", []string{"./polyfill.js", "pkg", "fs"}},
		{"rust use-tree", "rust", "use std::{fmt, io};\nuse crate::a::B;\n", []string{"std", "crate::a::B"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := extractImports(tt.lang, []byte(tt.src))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractImports = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDetectLanguageTS: .ts se enruta al grammar de JavaScript (limitación
// documentada — sintaxis exclusiva de TS ⇒ error de parseo ⇒ 0 símbolos).
func TestDetectLanguageTS(t *testing.T) {
	if got := detectLanguage("app.ts"); got != "js" {
		t.Errorf("detectLanguage(app.ts) = %q, want js", got)
	}
	src := []byte("interface SoloTS { a: string; }\n")
	rows, err := extractSymbols("js", src)
	if err == nil {
		t.Errorf("TS exclusivo parseó como JS sin error: %+v", rows)
	}
	if len(rows) != 0 {
		t.Errorf("TS exclusivo con símbolos: %+v", rows)
	}
}
