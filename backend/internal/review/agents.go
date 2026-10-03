// Loop de agentes del pipeline (mapa: backend.review — "código propio, ~200
// líneas, sin framework"): reparte los archivos del diff, llama al gateway
// con el system prompt embebido, parsea y valida la salida JSON contra los
// conjuntos cerrados (§9.8) y reintenta lo malformado antes de descartarlo.
// Sin reflexión, sin framework: un for, un json.Unmarshal y validación.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/Andres39128/codeowl/backend/prompts"
)

// Los prompts viven en archivos embebidos (§9.5: los prompts son código —
// jamás strings hardcodeados en Go): se cargan una vez al iniciar el paquete.
var (
	promptReviewer   = prompts.Reviewer()
	promptSummarizer = prompts.Summarizer()
	promptTestgen    = prompts.Testgen()
)

// roleReview es el rol del gateway para Reviewer y Summarizer (mapa:
// Reviewer/Summarizer → review).
const roleReview = "review"

// languagePlaceholder es el hueco de idioma de los system prompts (reviewer,
// summarizer y chat): fillLanguage lo llena con el idioma efectivo de la
// corrida (§9.5).
const languagePlaceholder = "{{LANGUAGE}}"

// fillLanguage llena el hueco de idioma del system prompt dado (mismo
// mecanismo que el chat, §3.3). El idioma efectivo jamás llega vacío: la
// resolución de la config cae a 'es' (§9.5).
func fillLanguage(prompt, language string) string {
	return strings.ReplaceAll(prompt, languagePlaceholder, language)
}

// Marcadores del prompt de usuario: enrutan la respuesta del gateway (los
// stubs de test los usan para saber a qué agente responden).
const (
	userFilePrefix    = "Archivo: "
	summarizerMarker  = "Resumí el siguiente pull request."
	testgenUserMarker = "Hallazgo de la revisión:"
)

// SummaryResult es la salida del Summarizer (contrato §9.8).
type SummaryResult struct {
	Summary     string `json:"summary"`
	Walkthrough string `json:"walkthrough"`
	Mermaid     string `json:"mermaid"`
}

// rawFinding es la salida cruda del Reviewer antes de validar.
type rawFinding struct {
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Body       string `json:"body"`
	Suggestion string `json:"suggestion"`
}

// Conjuntos cerrados de §3.3 — la salida del LLM no es confianza: todo
// finding se valida contra ellos antes de entrar al pipeline.
var (
	validSeverity = map[string]bool{"high": true, "medium": true, "low": true}
	validCategory = map[string]bool{
		"security": true, "logic": true, "performance": true,
		"style": true, "tests": true, "other": true,
	}
)

// runReviewer analiza cada archivo del diff con el agente Reviewer (una
// llamada LLM por archivo, en paralelo con tope de concurrencia — los slots
// por review los fija el gateway, §9.6). El system prompt viaja con el
// idioma efectivo ya lleno. Un archivo cuya salida no parsea tras
// cfg.AgentRetries reintentos se descarta con registro (§9.8: nunca
// crashea el job) y queda declarado como cobertura parcial. Devuelve error
// solo si el contexto se cancela (el job está muriendo).
func runReviewer(ctx context.Context, gw Gateway, cfg Config, language string, files map[string]string) ([]Finding, []string, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)
	system := fillLanguage(promptReviewer, language)

	var (
		mu        sync.Mutex
		findings  []Finding
		discarded []string
	)
	for file, hunks := range files {
		g.Go(func() error {
			fs, err := reviewFile(ctx, gw, cfg.AgentRetries, system, file, hunks)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				discarded = append(discarded, fmt.Sprintf("%s (%v)", file, err))
				return nil // el descarte no aborta la corrida (§9.8)
			}
			findings = append(findings, fs...)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, fmt.Errorf("esperando el análisis del reviewer: %w", err)
	}
	sortFindings(findings)
	sort.Strings(discarded)
	return findings, discarded, nil
}

// reviewFile analiza UN archivo: llamada LLM, parseo estricto y reintentos
// por salida malformada (§9.8). El fallo del gateway (failover agotado) no
// se reintenta acá — el gateway ya agotó los suyos (§9.7).
func reviewFile(ctx context.Context, gw Gateway, retries int, system, file, hunks string) ([]Finding, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		content, err := gw.Complete(ctx, roleReview, system, reviewerUser(file, hunks))
		if err != nil {
			return nil, fmt.Errorf("failover agotado: %w", err)
		}
		fs, perr := parseFindings(content, file)
		if perr == nil {
			return fs, nil
		}
		lastErr = perr
		slog.Warn("review: salida malformada del reviewer",
			"archivo", file, "intento", attempt+1, "error", perr)
	}
	return nil, fmt.Errorf("salida malformada tras %d reintentos: %w", retries, lastErr)
}

// runSummarizer genera el resumen del PR (§3.6.2) con el idioma efectivo en
// el system prompt. El mermaid se valida antes de devolverse: si no parsea,
// se omite con registro (§9.8).
func runSummarizer(ctx context.Context, gw Gateway, cfg Config, language, stats string, counts map[string]int) (*SummaryResult, error) {
	system := fillLanguage(promptSummarizer, language)
	var lastErr error
	for attempt := 0; attempt <= cfg.AgentRetries; attempt++ {
		content, err := gw.Complete(ctx, roleReview, system, summarizerUser(stats, counts))
		if err != nil {
			return nil, fmt.Errorf("failover agotado: %w", err)
		}
		s, perr := parseSummary(content)
		if perr == nil {
			if s.Mermaid != "" && !validMermaid(s.Mermaid) {
				slog.Warn("review: mermaid del summarizer no parsea, se omite (§9.8)")
				s.Mermaid = ""
			}
			return s, nil
		}
		lastErr = perr
		slog.Warn("review: salida malformada del summarizer", "intento", attempt+1, "error", perr)
	}
	return nil, fmt.Errorf("salida malformada tras %d reintentos: %w", cfg.AgentRetries, lastErr)
}

// runTestGenerator genera UNA prueba unitaria que documenta el hallazgo
// (comando /tests, F4). Sin reintentos: el chat publica lo que genere — el
// reintento del job regenera todo el lote.
func runTestGenerator(ctx context.Context, gw Gateway, f Finding, hunks, framework string) (string, error) {
	out, err := gw.Complete(ctx, roleReview, promptTestgen, testgenUser(f, hunks, framework))
	if err != nil {
		return "", fmt.Errorf("failover agotado: %w", err)
	}
	if strings.TrimSpace(stripFences(out)) == "" {
		return "", fmt.Errorf("salida vacía del generador de pruebas")
	}
	return out, nil
}

// testgenUser arma el prompt de usuario del generador de pruebas: hallazgo
// (con su corrección si la hay), framework detectado por extensión y los
// hunks del archivo — el contexto mínimo para una prueba que reproduce el
// problema.
func testgenUser(f Finding, hunks, framework string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s:%d — severidad %s, categoría %s.\n\n%s",
		testgenUserMarker, f.File, f.Line, f.Severity, f.Category, f.Body)
	if f.Suggestion != "" {
		b.WriteString("\n\nCorrección sugerida:\n\n```suggestion\n" + f.Suggestion + "\n```")
	}
	fmt.Fprintf(&b, "\n\nFramework de pruebas: %s.\n\n", framework)
	b.WriteString("Hunks del diff de " + f.File + ":\n\n```diff\n" + hunks + "```")
	return b.String()
}

// parseFindings valida la salida JSON del Reviewer para el archivo dado.
// Estricta: cualquier entry inválido invalida toda la salida → reintento
// (§9.8). El file de cada finding lo impone el pipeline: el agente solo vio
// ese archivo.
func parseFindings(content, file string) ([]Finding, error) {
	var raw []rawFinding
	if err := json.Unmarshal([]byte(stripFences(content)), &raw); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	out := make([]Finding, 0, len(raw))
	for i, r := range raw {
		switch {
		case r.Line <= 0:
			return nil, fmt.Errorf("finding %d: línea inválida %d", i, r.Line)
		case !validSeverity[r.Severity]:
			return nil, fmt.Errorf("finding %d: severity %q fuera del conjunto cerrado", i, r.Severity)
		case !validCategory[r.Category]:
			return nil, fmt.Errorf("finding %d: category %q fuera del conjunto cerrado", i, r.Category)
		case strings.TrimSpace(r.Body) == "":
			return nil, fmt.Errorf("finding %d: body vacío", i)
		}
		out = append(out, Finding{
			File:       file,
			Line:       int32(r.Line),
			Severity:   r.Severity,
			Category:   r.Category,
			Body:       r.Body,
			Suggestion: r.Suggestion,
			Source:     SourceLLM,
		})
	}
	return out, nil
}

// parseSummary valida la salida JSON del Summarizer: summary obligatorio.
func parseSummary(content string) (*SummaryResult, error) {
	var s SummaryResult
	if err := json.Unmarshal([]byte(stripFences(content)), &s); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if strings.TrimSpace(s.Summary) == "" {
		return nil, fmt.Errorf("summary vacío")
	}
	return &s, nil
}

// stripFences tolera el hábito de los modelos de envolver el JSON en bloques
// de código: no relaja la validación del contenido, solo el sobre del
// transporte.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// mermaidKinds son los encabezados de diagrama que Mermaid reconoce.
var mermaidKinds = []string{
	"sequenceDiagram", "graph", "flowchart", "stateDiagram", "classDiagram",
	"erDiagram", "gantt", "pie", "mindmap", "timeline",
}

// validMermaid es un chequeo estructural mínimo: primera línea no vacía con
// encabezado de diagrama conocido.
// ponytail: parser Mermaid completo si F3 lo exige — el render final lo hace
// el VCS en el PR, este guard solo evita publicar basura evidente.
func validMermaid(s string) bool {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		for _, k := range mermaidKinds {
			if strings.HasPrefix(l, k) {
				return true
			}
		}
		return false
	}
	return false
}

// reviewerUser arma el prompt de usuario del Reviewer: archivo + sus hunks
// completos (encabezados incluidos — el contexto ayuda al anclaje).
func reviewerUser(file, hunks string) string {
	return userFilePrefix + file + "\n\n```diff\n" + hunks + "```"
}

// summarizerUser arma el prompt de usuario del Summarizer: estadísticas del
// diff + recuento de hallazgos por severidad.
func summarizerUser(stats string, counts map[string]int) string {
	return summarizerMarker + "\n\n" + stats +
		"\n\nHallazgos de la revisión: " +
		fmt.Sprintf("%d alta, %d media, %d baja.", counts["high"], counts["medium"], counts["low"])
}

// sortFindings ordena por archivo, línea y categoría: salida determinista
// para publicación y tests.
func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return fs[i].Category < fs[j].Category
	})
}

func sortStrings(s []string) { sort.Strings(s) }

// splitDiffByFile parte el diff unificado por archivo: devuelve el chunk
// completo de cada archivo (para el prompt del Reviewer) y el conteo de
// líneas cambiadas (+/-, sin encabezados) por archivo y total (para los
// topes de §9.6). Los archivos eliminados (+++ /dev/null) no se analizan:
// no hay lado nuevo donde anclar.
func splitDiffByFile(diff string) (hunks map[string]string, changed map[string]int, total int) {
	hunks = map[string]string{}
	changed = map[string]int{}
	builders := map[string]*strings.Builder{}
	cur := ""
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur = "" // la ruta se confirma con el +++
		case strings.HasPrefix(l, "+++ "):
			path := strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if path == "/dev/null" {
				cur = ""
				continue
			}
			cur = path
			builders[cur] = &strings.Builder{}
			hunks[cur] = "" // se llena al final desde el builder
			builders[cur].WriteString(l + "\n")
		case cur != "":
			switch {
			case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):

				changed[cur]++
				total++
			case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
				changed[cur]++
				total++
			}
			builders[cur].WriteString(l + "\n")
		}
	}
	for f, b := range builders {
		hunks[f] = b.String()
	}
	return hunks, changed, total
}
