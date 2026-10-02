// Package analyze invoca el analyzer sandbox (imagen propia, §9.4) y
// normaliza su salida — findings SAST listos para dedup/persistencia.
// (mapa: backend.analyze). La extracción de símbolos (--symbols) se expone
// cuando F4 la consuma; acá solo corre el análisis de linters.
package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// runFlags de seguridad del sandbox (§9.4, no negociables): sin red, rootfs
// read-only, tope de procesos y de RAM. El workdir entra read-only y /tmp es
// un tmpfs con tope — ahí viven los caches de los linters que compilan.
const imageWorkdir = "/workdir"

// Config del invocador (etapa 2 de config: topes de pipeline).
type Config struct {
	Image     string        // ANALYZER_IMAGE
	Memory    string        // ANALYZER_MEMORY (formato podman: "1g", "512m")
	PidsLimit int           // ANALYZER_PIDS_LIMIT
	Timeout   time.Duration // ANALYZER_TIMEOUT
	TmpfsSize string        // ANALYZER_TMPFS_SIZE (tope del tmpfs de /tmp, §9.4)
}

// LinterStatus reporta qué corrió y qué no (§9.4: la ausencia de un linter
// jamás se interpreta como ausencia de hallazgos).
type LinterStatus struct {
	Name          string `json:"name"`
	Status        string `json:"status"` // ok | skipped | error
	Reason        string `json:"reason,omitempty"`
	FindingsCount int    `json:"findings_count"`
}

// Finding es el hallazgo SAST ya normalizado por el CLI (contrato de §3.3).
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"` // high | medium | low
	Category string `json:"category"` // security | logic | performance | style | tests | other
	Body     string `json:"body"`
	Source   string `json:"source"` // siempre "sast"
	Linter   string `json:"linter"`
	Rule     string `json:"rule"`
}

// AnalysisResult es la salida completa del analyzer.
type AnalysisResult struct {
	FilesAnalyzed int            `json:"files_analyzed"`
	LintersRun    []LinterStatus `json:"linters_run"`
	Findings      []Finding      `json:"findings"`
}

// conjuntos cerrados de §3.3 — la salida del sandbox no es confianza: se
// valida antes de devolverla.
var (
	validSeverity = map[string]bool{"high": true, "medium": true, "low": true}
	validCategory = map[string]bool{
		"security": true, "logic": true, "performance": true,
		"style": true, "tests": true, "other": true,
	}
)

// Runner invoca el analyzer en un workdir.
type Runner struct {
	config Config
}

// NewRunner crea un Runner con la config de etapa 2.
func NewRunner(cfg Config) *Runner { return &Runner{config: cfg} }

// Run analiza workdir con la imagen sandbox y devuelve el resultado validado.
// El workdir se monta read-only: el sandbox nunca escribe el clon (§9.4).
func (r *Runner) Run(ctx context.Context, workdir string) (*AnalysisResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "podman", buildRunArgs(workdir, r.config)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("analyzer: %s", truncate(msg, 500))
	}

	res, err := Parse([]byte(stdout.String()))
	if err != nil {
		return nil, err
	}
	return res, nil
}

// buildRunArgs construye el podman run del sandbox. Función aparte para
// testear los flags de §9.4 sin ejecutar podman.
func buildRunArgs(workdir string, cfg Config) []string {
	return []string{
		"run", "--rm",
		"--network=none",                            // §9.4: sin red
		"--read-only",                               // §9.4: rootfs read-only
		"--pids-limit", strconv.Itoa(cfg.PidsLimit), // §9.4: tope de procesos
		"--memory", cfg.Memory, // §9.4: tope de RAM
		"--tmpfs", "/tmp:size=" + cfg.TmpfsSize, // §9.4: caches de linters compiladores
		"-v", workdir + ":" + imageWorkdir + ":ro",
		cfg.Image,
		imageWorkdir,
	}
}

// Parse valida y tipa la salida JSON del analyzer. Falla si algún finding
// viola los conjuntos cerrados de severity/category o el source fijo.
func Parse(data []byte) (*AnalysisResult, error) {
	var res AnalysisResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("salida del analyzer no es JSON válido: %w", err)
	}
	if res.LintersRun == nil {
		return nil, fmt.Errorf("salida del analyzer sin linters_run")
	}
	if res.Findings == nil {
		res.Findings = []Finding{}
	}
	for i, f := range res.Findings {
		if !validSeverity[f.Severity] {
			return nil, fmt.Errorf("finding %d (%s): severity %q fuera del conjunto cerrado", i, f.File, f.Severity)
		}
		if !validCategory[f.Category] {
			return nil, fmt.Errorf("finding %d (%s): category %q fuera del conjunto cerrado", i, f.File, f.Category)
		}
		if f.Source != "sast" {
			return nil, fmt.Errorf("finding %d (%s): source %q, se espera \"sast\"", i, f.File, f.Source)
		}
		if f.Linter == "" || f.File == "" {
			return nil, fmt.Errorf("finding %d: file y linter son obligatorios", i)
		}
		// §9.4: el enmascarado del valor de un secret vive en el analyzer
		// (CLI, --redact) y en el publicador (T5, todo comentario). Acá la
		// salida ya no debería traer valores: nadie serializa Secret.
	}
	return &res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
