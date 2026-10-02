// Comando analyzer: CLI del sandbox (mapa: analyzer.cli). Analiza el repo
// montado en /workdir con los linters de la imagen y emite UN JSON normalizado
// por stdout (§9.4): qué linter corrió, cuál se saltó y por qué — la ausencia
// de un linter jamás cuenta como ausencia de hallazgos.
//
// Uso: analyzer /workdir [--symbols]
//
// Sin red, con rootfs read-only: los caches de los linters que compilan
// (GOCACHE, CARGO_HOME) viven en el tmpfs de /tmp montado por el invocador.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sourceValue es el único valor de source para findings SAST (§3.3: conjunto
// cerrado 'llm' | 'sast').
const sourceValue = "sast"

// lintersOrder fija el orden de reporte, independiente del resultado.
var lintersOrder = []string{"eslint", "ruff", "golangci-lint", "clippy", "gitleaks"}

// skipDirs no se recorren: VCS interno y artefactos de dependencias/build
// (lintear vendor/node_modules ensucia la salida con código de terceros).
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".venv": true, "venv": true, "__pycache__": true,
}

// linterStatus reporta el resultado de un linter (§9.4: qué corrió y qué no).
type linterStatus struct {
	Name          string `json:"name"`
	Status        string `json:"status"` // ok | skipped | error
	Reason        string `json:"reason,omitempty"`
	FindingsCount int    `json:"findings_count"`
}

// finding es el contrato normalizado de hallazgo (§3.3: severity y category
// son conjuntos cerrados; source siempre "sast" acá).
type finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"` // high | medium | low
	Category string `json:"category"` // security | logic | performance | style | tests | other
	Body     string `json:"body"`
	Source   string `json:"source"`
	Linter   string `json:"linter"`
	Rule     string `json:"rule"`
}

// report es el JSON completo que sale por stdout.
type report struct {
	FilesAnalyzed int            `json:"files_analyzed"`
	LintersRun    []linterStatus `json:"linters_run"`
	Findings      []finding      `json:"findings"`
}

// symbolFile es la entrada del stub de símbolos (F4 trae el árbol completo
// vía tree-sitter; por ahora: archivo + lenguaje).
type symbolFile struct {
	Path     string `json:"path"`
	Language string `json:"language"`
}

// symbolReport es el JSON del modo --symbols.
type symbolReport struct {
	FilesAnalyzed int          `json:"files_analyzed"`
	Files         []symbolFile `json:"files"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "analyzer:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	var workdir string
	symbols := false
	for _, a := range args {
		switch a {
		case "--symbols":
			symbols = true
		case "-h", "--help":
			fmt.Fprintln(stderr, "uso: analyzer /workdir [--symbols]")
			return nil
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("flag desconocida %q", a)
			}
			if workdir != "" {
				return fmt.Errorf("se espera un solo directorio, recibí %q y %q", workdir, a)
			}
			workdir = a
		}
	}
	if workdir == "" {
		return fmt.Errorf("falta el directorio a analizar: analyzer /workdir [--symbols]")
	}
	info, err := os.Stat(workdir)
	if err != nil {
		return fmt.Errorf("workdir inaccesible: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q no es un directorio", workdir)
	}

	files, err := walk(workdir)
	if err != nil {
		return err
	}

	if symbols {
		return emitSymbolReport(files, stdout)
	}
	return emitLintReport(workdir, files, stdout)
}

// walk recorre workdir y clasifica cada archivo por extensión. La detección es
// por extensión: alcanza para enrutar los 5 linters del stack (§2); go-enry
// entra con F4 si la extracción de símbolos necesita más precisión.
func walk(root string) ([]symbolFile, error) {
	var files []symbolFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if lang := detectLanguage(d.Name()); lang != "" {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, symbolFile{Path: rel, Language: lang})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recorrido de %s: %w", root, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func detectLanguage(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "go"
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
		return "js"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}

func emitSymbolReport(files []symbolFile, stdout io.Writer) error {
	out, err := json.Marshal(symbolReport{FilesAnalyzed: len(files), Files: files})
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(out, '\n'))
	return err
}

func emitLintReport(workdir string, files []symbolFile, stdout io.Writer) error {
	byLang := map[string][]string{} // lenguaje → rutas relativas
	for _, f := range files {
		byLang[f.Language] = append(byLang[f.Language], f.Path)
	}

	rep := report{FilesAnalyzed: len(files), Findings: []finding{}}
	statuses := map[string]linterStatus{}

	// Siempre corren (sin grafo de dependencias): ESLint, Ruff, Gitleaks (§9.4).
	rep.Findings = append(rep.Findings, runLinter(statuses, "eslint", func() ([]finding, error) {
		return runESLint(workdir, byLang["js"])
	}, len(byLang["js"]) > 0, "no JS/TS files detected")...)
	rep.Findings = append(rep.Findings, runLinter(statuses, "ruff", func() ([]finding, error) {
		return runRuff(workdir, byLang["python"])
	}, len(byLang["python"]) > 0, "no Python files detected")...)
	rep.Findings = append(rep.Findings, runLinter(statuses, "gitleaks", func() ([]finding, error) {
		return runGitleaks(workdir)
	}, true, "")...)

	// Corren solo si las dependencias están en el clon: sin red no se
	// descargan (§9.4 best-effort).
	golangciDeps := fileExists(filepath.Join(workdir, "go.sum")) &&
		dirExists(filepath.Join(workdir, "vendor"))
	rep.Findings = append(rep.Findings, runLinter(statuses, "golangci-lint", func() ([]finding, error) {
		return runGolangCILint(workdir)
	}, len(byLang["go"]) > 0 && golangciDeps, "no dependencies available")...)

	clippyDeps := fileExists(filepath.Join(workdir, "Cargo.lock")) &&
		(dirExists(filepath.Join(workdir, "target")) || dirExists(filepath.Join(workdir, "vendor")))
	rep.Findings = append(rep.Findings, runLinter(statuses, "clippy", func() ([]finding, error) {
		return runClippy(workdir)
	}, len(byLang["rust"]) > 0 && clippyDeps, "no dependencies available")...)

	for _, name := range lintersOrder {
		rep.LintersRun = append(rep.LintersRun, statuses[name])
	}

	out, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(out, '\n'))
	return err
}

// runLinter ejecuta fn, carga el estado del linter y devuelve los hallazgos.
// available=false → skipped con razón; error de ejecución → status "error"
// con detalle, sin frenar a los demás linters.
func runLinter(statuses map[string]linterStatus, name string, fn func() ([]finding, error), available bool, skipReason string) []finding {
	st := linterStatus{Name: name}
	if !available {
		if skipReason == "" {
			skipReason = "not applicable"
		}
		st.Status, st.Reason = "skipped", skipReason
		statuses[name] = st
		return nil
	}
	findings, err := fn()
	if err != nil {
		st.Status, st.Reason = "error", truncate(err.Error(), 300)
		statuses[name] = st
		return nil
	}
	st.Status, st.FindingsCount = "ok", len(findings)
	statuses[name] = st
	return findings
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
