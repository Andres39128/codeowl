// Invocación y parseo de los 5 linters del stack (§2). Cada uno corre con el
// config propio de la imagen, pasado explícito — los configs del repo
// revisado se ignoran (§9.4: un config de repo es código arbitrario en el
// sandbox y permite silenciar la propia detección).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dirConfigs es donde el Containerfile copia los configs de la imagen.
const dirConfigs = "/opt/linters"

// exitOK son los códigos de salida que significan "corrió y reportó":
// 0 = limpio, 1 = hay hallazgos (convención de eslint/ruff/golangci/gitleaks).
func exitOK(code int) bool { return code == 0 || code == 1 }

// decodeFirstJSON extrae el primer valor JSON de la salida y descarta el
// resto: golangci-lint v2 pega un resumen de texto después del documento JSON.
func decodeFirstJSON(out string, v any) error {
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("salida no es JSON válido: %w", err)
	}
	return nil
}

// relToWorkdir normaliza rutas de linters (absolutas, relativas al cwd o con
// ../workdir/ como golangci-lint) a la forma del contrato: relativa al workdir.
func relToWorkdir(workdir, raw string) string {
	if raw == "" {
		return raw
	}
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(workdir, p)
	}
	if rel, err := filepath.Rel(workdir, filepath.Clean(p)); err == nil {
		return rel
	}
	return raw
}

// outJSON ejecuta cmd y devuelve su stdout. El stderr se captura para el
// detalle de error; nada de los linters sale por stdout del CLI salvo el
// JSON final.
func outJSON(cmd *exec.Cmd) (string, error) {
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitOK(exitErr.ExitCode()) {
			return stdout.String(), nil // hallazgos presentes: salida normal
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", cmd.Path, msg)
	}
	return stdout.String(), nil
}

// --- ESLint (JS/TS; corre siempre que haya archivos) -------------------------

type eslintMessage struct {
	RuleID   string `json:"ruleId"`
	Severity int    `json:"severity"` // 2=error, 1=warning
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

type eslintFile struct {
	FilePath string          `json:"filePath"`
	Messages []eslintMessage `json:"messages"`
}

func runESLint(workdir string, files []string) ([]finding, error) {
	if len(files) == 0 {
		return nil, nil
	}
	// cwd=workdir: el basePath de ESLint es el cwd y los archivos de afuera
	// los ignora. El config es absoluto y de la imagen (--config desactiva la
	// búsqueda de configs del repo — §9.4).
	cmd := exec.Command("eslint",
		"--config", filepath.Join(dirConfigs, "eslint.config.mjs"),
		"--format", "json",
	)
	abs := make([]string, len(files))
	for i, f := range files {
		abs[i] = filepath.Join(workdir, f)
	}
	cmd.Args = append(cmd.Args, abs...)
	cmd.Dir = workdir
	out, err := outJSON(cmd)
	if err != nil {
		return nil, err
	}
	var parsed []eslintFile
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("salida ESLint no es JSON válido: %w", err)
	}
	var findings []finding
	for _, f := range parsed {
		for _, m := range f.Messages {
			findings = append(findings, finding{
				File: relToWorkdir(workdir, f.FilePath), Line: m.Line,
				Severity: severityFromESLint(m.Severity),
				Category: classifyESLint(m.RuleID),
				Body:     m.Message, Source: sourceValue,
				Linter: "eslint", Rule: ruleOr(m.RuleID, "syntax"),
			})
		}
	}
	return findings, nil
}

func ruleOr(rule, def string) string {
	if rule == "" {
		return def
	}
	return rule
}

// --- Ruff (Python; corre siempre) --------------------------------------------

type ruffDiagnostic struct {
	Code     *string `json:"code"`
	Message  string  `json:"message"`
	Filename string  `json:"filename"`
	Location struct {
		Row int `json:"row"`
	} `json:"location"`
}

func runRuff(workdir string, files []string) ([]finding, error) {
	if len(files) == 0 {
		return nil, nil
	}
	abs := make([]string, len(files))
	for i, f := range files {
		abs[i] = filepath.Join(workdir, f)
	}
	// --config explícito: ignora pyproject.toml/ruff.toml del repo (§9.4).
	cmd := exec.Command("ruff", "check",
		"--config", filepath.Join(dirConfigs, "ruff.toml"),
		"--output-format", "json",
		"--no-cache",
	)
	cmd.Args = append(cmd.Args, abs...)
	cmd.Dir = "/tmp"
	out, err := outJSON(cmd)
	if err != nil {
		return nil, err
	}
	var parsed []ruffDiagnostic
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("salida Ruff no es JSON válido: %w", err)
	}
	findings := make([]finding, 0, len(parsed))
	for _, d := range parsed {
		code := ""
		if d.Code != nil {
			code = *d.Code
		}
		findings = append(findings, finding{
			File: relToWorkdir(workdir, d.Filename), Line: d.Location.Row,
			Severity: ruffSeverity(code),
			Category: classifyRuff(code),
			Body:     d.Message, Source: sourceValue,
			Linter: "ruff", Rule: ruleOr(code, "syntax"),
		})
	}
	return findings, nil
}

// --- golangci-lint (Go; solo con vendor/) -------------------------------------

type golangciIssue struct {
	FromLinter string `json:"FromLinter"`
	Text       string `json:"Text"`
	Severity   string `json:"Severity"`
	Pos        struct {
		Filename string `json:"Filename"`
		Line     int    `json:"Line"`
	} `json:"Pos"`
}

func runGolangCILint(workdir string) ([]finding, error) {
	// Vendor mode: sin red, el módulo compila solo con vendor/ (§9.4). El
	// formato JSON sale por config de la imagen (.golangci.yml → output.formats).
	cmd := exec.Command("golangci-lint", "run",
		"--config", filepath.Join(dirConfigs, ".golangci.yml"),
	)
	cmd.Dir = workdir
	cmd.Env = envWith(os.Environ(),
		"GOFLAGS=-mod=vendor",
		"GOCACHE=/tmp/go-build",
		"GOPATH=/tmp/gopath",
	)
	out, err := outJSON(cmd)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Issues []golangciIssue `json:"Issues"`
	}
	if err := decodeFirstJSON(out, &parsed); err != nil {
		return nil, fmt.Errorf("salida golangci-lint: %w", err)
	}
	findings := make([]finding, 0, len(parsed.Issues))
	for _, iss := range parsed.Issues {
		findings = append(findings, finding{
			File: relToWorkdir(workdir, iss.Pos.Filename), Line: iss.Pos.Line,
			Severity: golangciSeverity(iss.Severity),
			Category: classifyGolangCI(iss.FromLinter),
			Body:     iss.Text, Source: sourceValue,
			Linter: "golangci-lint", Rule: iss.FromLinter,
		})
	}
	return findings, nil
}

// --- Clippy (Rust; solo con deps compilables offline) -------------------------

type cargoMessage struct {
	Reason  string `json:"reason"`
	Message struct {
		Level    string `json:"level"`
		Rendered string `json:"rendered"`
		Code     *struct {
			Code string `json:"code"`
		} `json:"code"`
		Spans []struct {
			IsPrimary bool   `json:"is_primary"`
			FileName  string `json:"file_name"`
			LineStart int    `json:"line_start"`
		} `json:"spans"`
	} `json:"message"`
}

func runClippy(workdir string) ([]finding, error) {
	cmd := exec.Command("cargo", "clippy", "--message-format", "json", "--quiet")
	cmd.Dir = workdir
	cmd.Env = envWith(os.Environ(),
		"CARGO_HOME=/tmp/cargo",
		"CARGO_TARGET_DIR=/tmp/cargo-target",
		"CLIPPY_CONF_DIR="+dirConfigs, // config propio de la imagen, no del repo (§9.4)
	)
	out, err := outJSON(cmd)
	if err != nil {
		return nil, err
	}
	var findings []finding
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m cargoMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // línea no-JSON del build: no es un hallazgo
		}
		if m.Reason != "compiler-message" || (m.Message.Level != "error" &&
			m.Message.Level != "warning" && m.Message.Level != "note") {
			continue
		}
		file, lineNum := "", 0
		for _, s := range m.Message.Spans {
			if s.IsPrimary {
				file, lineNum = s.FileName, s.LineStart
				break
			}
		}
		rule := "rustc"
		if m.Message.Code != nil {
			rule = m.Message.Code.Code
		}
		findings = append(findings, finding{
			File: relToWorkdir(workdir, file), Line: lineNum,
			Severity: clippySeverity(m.Message.Level),
			Category: classifyClippy(rule),
			Body:     m.Message.Rendered, Source: sourceValue,
			Linter: "clippy", Rule: rule,
		})
	}
	return findings, nil
}

// --- Gitleaks (secrets; corre siempre sobre el clon) --------------------------

type gitleaksFinding struct {
	Description string `json:"Description"`
	StartLine   int    `json:"StartLine"`
	File        string `json:"File"`
	RuleID      string `json:"RuleID"`
	Secret      string `json:"Secret"` // solo entra para enmascarar; jamás sale
	Match       string `json:"Match"`  // ídem
}

func runGitleaks(workdir string) ([]finding, error) {
	reportPath := "/tmp/gitleaks-report.json"
	// Modo dir: escanea el árbol sin depender de .git; sobre clon shallow es
	// equivalente (solo ve el tramo traído — tradeoff §9.4). --redact y el
	// parseo que ignora Secret/Match garantizan que el valor jamás salga.
	cmd := exec.Command("gitleaks", "dir", workdir,
		"--config", filepath.Join(dirConfigs, ".gitleaks.toml"),
		"--report-format", "json",
		"--report-path", reportPath,
		"--redact",
		"--exit-code", "0",
		"--no-banner",
	)
	cmd.Dir = "/tmp"
	if _, err := outJSON(cmd); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, fmt.Errorf("reporte de gitleaks no generado: %w", err)
	}
	var parsed []gitleaksFinding
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("reporte de gitleaks no es JSON válido: %w", err)
	}
	findings := make([]finding, 0, len(parsed))
	for _, g := range parsed {
		findings = append(findings, finding{
			File: relToWorkdir(workdir, g.File), Line: g.StartLine,
			Severity: "high", // un secret es siempre high (contrato)
			Category: "security",
			// Solo ruta, línea y regla sobreviven — jamás el valor detectado.
			Body:   maskSecret(g.Description, g.Secret, g.Match),
			Source: sourceValue,
			Linter: "gitleaks", Rule: g.RuleID,
		})
	}
	return findings, nil
}
