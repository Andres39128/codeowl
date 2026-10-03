// Extracción de símbolos e imports vía tree-sitter (§6 F4, mapa analyzer.cli:
// "modo --symbols: {file, symbol, kind, start_line, end_line, imports}[]" —
// insumo del chunking de internal/index). cgo vive solo acá y solo corre
// dentro del contenedor del analyzer (§3.2: el backend consume el JSON del
// CLI, jamás gana dependencias de tree-sitter).
//
// Pines (§9.4: reproducibilidad — en src/go.mod, verificado contra proxy):
//   github.com/tree-sitter/go-tree-sitter v0.24.0      (runtime, última release)
//   github.com/tree-sitter/tree-sitter-go v0.23.4
//   github.com/tree-sitter/tree-sitter-javascript v0.23.1
//   github.com/tree-sitter/tree-sitter-python v0.23.6
//   github.com/tree-sitter/tree-sitter-rust v0.23.3
// Los tags v0.25.x de los grammars se generaron con tree-sitter CLI 0.25
// (ABI 15) y el runtime Go v0.24.0 solo soporta ABI 13-14: SetLanguage falla.
// Bump de grammars = esperar runtime Go con ABI 15 (PR propio).
//
// Mapeo de node-types → kind (conjunto cerrado de §3.3):
//   go:     function_declaration→function, method_declaration→method,
//           type_spec(struct_type)→type, type_spec(interface_type)→interface,
//           type_spec(otro)→type
//   js:     function_declaration / generator_function_declaration→function,
//           class_declaration→class, method_definition→method
//   python: function_definition→function (method si cuelga de una clase),
//           class_definition→class
//   rust:   function_item→function (method si cuelga de un impl),
//           struct_item/enum_item/type_item/union_item→type, trait_item→trait
// Limitaciones documentadas: .ts/.tsx se parsean con el grammar de JavaScript
// (sintaxis exclusiva de TS → error de parseo → 0 símbolos); solo
// declaraciones — arrow functions, function expressions y closure defs de
// una línea no son símbolos.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

// symbolReport es el JSON del modo --symbols. Files se mantiene por
// compatibilidad con el stub anterior; Symbols es el contrato nuevo.
type symbolReport struct {
	FilesAnalyzed int          `json:"files_analyzed"`
	Files         []symbolFile `json:"files"`
	Symbols       []symbolRow  `json:"symbols"`
}

// symbolRow es una fila del contrato --symbols. imports es a nivel de archivo
// y se repite en cada símbolo del mismo (salida plana por símbolo).
type symbolRow struct {
	File      string   `json:"file"`
	Symbol    string   `json:"symbol"`
	Kind      string   `json:"kind"` // function|method|class|type|interface|trait (§3.3: cerrado)
	StartLine int      `json:"start_line"`
	EndLine   int      `json:"end_line"`
	Imports   []string `json:"imports"`
}

// errSyntaxError marca archivos que no parsean: el archivo sale con cero
// símbolos y el run sigue (un archivo roto nunca mata la indexación).
var errSyntaxError = errors.New("archivo con errores de sintaxis")

// languageBinding devuelve el grammar pinneado para el lenguaje detectado.
func languageBinding(lang string) *sitter.Language {
	switch lang {
	case "go":
		return sitter.NewLanguage(tsgo.Language())
	case "js":
		return sitter.NewLanguage(tsjs.Language()) // también .ts/.tsx (limitación documentada)
	case "python":
		return sitter.NewLanguage(tspy.Language())
	case "rust":
		return sitter.NewLanguage(tsrust.Language())
	default:
		return nil
	}
}

// extractSymbols parsea src con el grammar del lenguaje y extrae los
// símbolos en orden de documento (determinista). File lo completa el
// llamador: la extracción es por contenido.
func extractSymbols(lang string, src []byte) ([]symbolRow, error) {
	binding := languageBinding(lang)
	if binding == nil {
		return nil, fmt.Errorf("lenguaje sin grammar: %q", lang)
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(binding); err != nil {
		return nil, fmt.Errorf("grammar incompatible: %w", err)
	}
	tree := parser.ParseCtx(context.Background(), src, nil)
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil, errSyntaxError
	}
	var rows []symbolRow
	walkSymbols(root, src, "", &rows)
	// imports a nivel de archivo: se repiten en cada fila del archivo
	// (contrato: salida plana por símbolo).
	imports := extractImports(lang, src)
	for i := range rows {
		rows[i].Imports = imports
	}
	return rows, nil
}

// walkSymbols recorre el AST en profundidad. ctx marca el contenedor para
// distinguir method de function: "class" (python) e "impl" (rust); entrar al
// cuerpo de una función resetea el contexto.
func walkSymbols(n *sitter.Node, src []byte, ctx string, out *[]symbolRow) {
	kind, name := symbolOf(n, src, ctx)
	if kind != "" && name != "" {
		*out = append(*out, symbolRow{
			Symbol:    name,
			Kind:      kind,
			StartLine: int(n.StartPosition().Row) + 1,
			EndLine:   int(n.EndPosition().Row) + 1,
		})
	}
	inner := ctx
	switch n.Kind() {
	case "class_definition", "class": // python contenedor de métodos
		inner = "class"
	case "impl_item": // rust contenedor de métodos
		inner = "impl"
	default:
		if kind == "function" || kind == "method" {
			inner = "" // funciones anidadas no heredan el contexto
		}
	}
	for i := range n.NamedChildCount() {
		walkSymbols(n.NamedChild(i), src, inner, out)
	}
}

// symbolOf mapea un nodo a (kind, nombre) según el lenguaje (ver mapeo en el
// header). Devuelve ("", "") para nodos que no son símbolos.
func symbolOf(n *sitter.Node, src []byte, ctx string) (string, string) {
	nameNode := n.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nodeText(nameNode, src)
	}
	switch n.Kind() {
	case "function_declaration", "generator_function_declaration":
		// go y js comparten el node-type de función declarada
		return "function", name
	// Go
	case "method_declaration":
		return "method", name
	case "type_spec":
		if t := n.ChildByFieldName("type"); t != nil && t.Kind() == "interface_type" {
			return "interface", name
		}
		return "type", name
	case "class_declaration":
		return "class", name
	case "method_definition":
		return "method", name
	// Python
	case "function_definition":
		if ctx == "class" {
			return "method", name
		}
		return "function", name
	case "class_definition":
		return "class", name
	// Rust
	case "function_item", "function_signature_item": // firma = fn de trait sin cuerpo
		if ctx == "impl" {
			return "method", name
		}
		return "function", name
	case "struct_item", "enum_item", "type_item", "union_item":
		return "type", name
	case "trait_item":
		return "trait", name
	default:
		return "", ""
	}
}

func nodeText(n *sitter.Node, src []byte) string {
	return string(src[n.StartByte():n.EndByte()])
}

// --- imports: extracción best-effort por regex (§6 F4: los imports son
// patrones sintácticos simples; tree-sitter queda para los símbolos, que
// necesitan posiciones) -------------------------------------------------------

var (
	goImportSingle = regexp.MustCompile(`(?m)^\s*import\s+(?:[_\.]\s+|\w+\s+)?"([^"]+)"`)
	goImportBlock  = regexp.MustCompile(`(?s)import\s*\((.*?)\)`)
	goImportSpec   = regexp.MustCompile(`(?m)^\s*(?:[_\.]\s+|\w+\s+)?"([^"]+)"`)

	jsImportFrom = regexp.MustCompile(`import\s+[^;'"]*?\bfrom\s*['"]([^'"]+)['"]`)
	jsImportSide = regexp.MustCompile(`(?m)^\s*import\s*['"]([^'"]+)['"]`)
	jsImportReq  = regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`)

	pyImportPlain = regexp.MustCompile(`(?m)^\s*import\s+([\w.]+(?:\s+as\s+\w+)?(?:\s*,\s*[\w.]+(?:\s+as\s+\w+)?)*)`)
	pyImportFrom  = regexp.MustCompile(`(?m)^\s*from\s+([\w.]+)\s+import\b`)

	rustUse = regexp.MustCompile(`(?m)^\s*use\s+((?:::)?(?:\w+::)*\w+)`)
)

// importMatch es un import crudo con su posición: para devolverlos en orden
// de aparición cuando un lenguaje usa varios patrones (js: from/side-effect/
// require).
type importMatch struct {
	pos  int
	path string
}

// extractImports devuelve los imports del archivo, en orden de aparición y
// sin duplicados. Best-effort: un falso positivo vive en imports, no en
// símbolos (tradeoff aceptado y documentado en el header).
func extractImports(lang string, src []byte) []string {
	text := string(src)
	var matches []importMatch
	collect := func(re *regexp.Regexp, group int, transform func(string) string) {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			raw := text[m[group*2]:m[group*2+1]]
			if transform != nil {
				raw = transform(raw)
			}
			if p := strings.TrimSpace(raw); p != "" {
				matches = append(matches, importMatch{pos: m[0], path: p})
			}
		}
	}
	switch lang {
	case "go":
		collect(goImportSingle, 1, nil)
		for _, m := range goImportBlock.FindAllStringSubmatchIndex(text, -1) {
			block := text[m[2]:m[3]]
			for _, spec := range goImportSpec.FindAllStringSubmatchIndex(block, -1) {
				if p := strings.TrimSpace(block[spec[2]:spec[3]]); p != "" {
					matches = append(matches, importMatch{pos: m[0] + spec[0], path: p})
				}
			}
		}
	case "js":
		collect(jsImportFrom, 1, nil)
		collect(jsImportSide, 1, nil)
		collect(jsImportReq, 1, nil)
	case "python":
		// "import a as b, c.d" → a, c.d (el alias no es parte del módulo)
		module := func(list string) []string {
			var out []string
			for _, mod := range strings.Split(list, ",") {
				if f := strings.Fields(mod); len(f) > 0 {
					out = append(out, f[0])
				}
			}
			return out
		}
		collect(pyImportFrom, 1, nil)
		for _, m := range pyImportPlain.FindAllStringSubmatchIndex(text, -1) {
			for _, p := range module(text[m[2]:m[3]]) {
				matches = append(matches, importMatch{pos: m[0], path: p})
			}
		}
	case "rust":
		collect(rustUse, 1, nil)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].pos < matches[j].pos })
	imports := []string{}
	for _, m := range matches {
		if !contains(imports, m.path) {
			imports = append(imports, m.path)
		}
	}
	return imports
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// emitSymbolReport recorre los archivos de código, extrae símbolos e imports
// y escribe el JSON del contrato en stdout. Un archivo que no parsea sale con
// cero símbolos y el motivo va a stderr: el run nunca falla por un archivo
// roto (§6 F4 — la indexación no se puede frenar por un archivo malo).
func emitSymbolReport(workdir string, files []symbolFile, stderr, stdout io.Writer) error {
	rep := symbolReport{
		FilesAnalyzed: len(files),
		Files:         files,
		Symbols:       []symbolRow{},
	}
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(workdir, f.Path))
		if err != nil {
			fmt.Fprintf(stderr, "analyzer: símbolos omitidos %s: %v\n", f.Path, err)
			continue
		}
		rows, err := extractSymbols(f.Language, src)
		if err != nil {
			fmt.Fprintf(stderr, "analyzer: símbolos omitidos %s: %v\n", f.Path, err)
			continue
		}
		for _, r := range rows {
			r.File = f.Path
			rep.Symbols = append(rep.Symbols, r)
		}
	}
	out, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(out, '\n'))
	return err
}
