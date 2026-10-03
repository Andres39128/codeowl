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

	"github.com/Andres39128/codeowl/backend/internal/repoconfig"
	"github.com/Andres39128/codeowl/backend/prompts"
)

// Los prompts viven en archivos embebidos (§9.5: los prompts son código —
// jamás strings hardcodeados en Go): se cargan una vez al iniciar el paquete.
var (
	promptReviewer   = prompts.Reviewer()
	promptSummarizer = prompts.Summarizer()
	promptTestgen    = prompts.Testgen()
	promptVerifier   = prompts.Verifier()
	promptPremerge   = prompts.Premerge()
)

// roleReview es el rol del gateway para Reviewer y Summarizer (mapa:
// Reviewer/Summarizer → review).
const roleReview = "review"

// roleCheap es el rol del gateway para el Verifier (mapa: Verifier → cheap,
// §9.6): cross-check mecánico con modelo económico. Sin proveedor del rol
// el verifier no corre — degrada, jamás bloquea (§9.6).
const roleCheap = "cheap"

// retrievalQueryMaxChars acota la QUERY del retrieval (§6 F4): los hunks ya
// pasaron los topes de diff upstream; los primeros 2000 chars de código
// bastan para anclar la búsqueda de similitud.
// ponytail: query fija de 2000 chars — si la calidad de la similitud lo
// pide, se vuelve config.
const retrievalQueryMaxChars = 2000

// relatedContextHeader encabeza el bloque de símbolos relacionados (§6 F4)
// en el prompt de usuario del Reviewer.
const relatedContextHeader = "\n\n## Símbolos relacionados del repositorio\n"

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

// instructionsPlaceholder es el hueco de las reglas de revisión del repo
// (§9.5): fillInstructions lo llena con el bloque del mantenedor o lo vacía.
const instructionsPlaceholder = "{{INSTRUCTIONS}}"

// nitsPlaceholder es el hueco del modo estricto (§6 F3): fillNits agrega la
// regla de nits solo con perfil strict.
const nitsPlaceholder = "{{NITS}}"

// fillInstructions llena el hueco de reglas del repo con la guía del
// mantenedor (input confiable: review.yaml leído del merge-base, §9.5 — el
// PR no puede escribir sus propias reglas). Vacío → sin bloque y sin
// residuo del placeholder. El bloque va enmarcado como reglas de revisión
// del repo: autoridad de mantenedor sobre QUÉ mirar, no órdenes del diff.
func fillInstructions(prompt, instructions string) string {
	block := ""
	if strings.TrimSpace(instructions) != "" {
		block = "\n## Reglas de revisión de este repositorio\n\nEl mantenedor pidió prestar atención especial a lo siguiente al revisar:\n\n" + instructions + "\n"
	}
	return strings.ReplaceAll(prompt, instructionsPlaceholder, block)
}

// fillNits agrega la regla de nits al prompt solo con perfil strict (§6 F3):
// pide reportar también lo menor/trivial sin tocar el conjunto cerrado de
// severidades — los nits llegan como findings low. Otro perfil → hueco
// vacío, sin residuo.
func fillNits(prompt, profile string) string {
	block := ""
	if profile == "strict" {
		block = "- Además de los problemas reales, reportá también nits: detalles menores o triviales que valga la pena pulir (nombrado, claridad, estilo). Marcalos con severity \"low\"."
	}
	return strings.ReplaceAll(prompt, nitsPlaceholder, block)
}

// Marcadores del prompt de usuario: enrutan la respuesta del gateway (los
// stubs de test los usan para saber a qué agente responden).
const (
	userFilePrefix     = "Archivo: "
	summarizerMarker   = "Resumí el siguiente pull request."
	testgenUserMarker  = "Hallazgo de la revisión:"
	verifierUserMarker = "Verificá los siguientes hallazgos."
	premergeMarker     = "Evaluá el veredicto pre-merge del siguiente pull request."
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
// idioma efectivo, las reglas del repo y el modo del perfil ya llenos
// (§9.5/§6 F3). Cada archivo viaja con su bloque "Símbolos relacionados"
// del índice RAG (§6 F4): la retrieval corre dentro de este errgroup (los
// embed quedan acotados por el semáforo global del gateway). Un archivo
// cuya salida no parsea tras cfg.AgentRetries reintentos se descarta con
// registro (§9.8: nunca crashea el job) y queda declarado como cobertura
// parcial. Devuelve error solo si el contexto se cancela (el job está
// muriendo).
func runReviewer(ctx context.Context, gw Gateway, retr Retriever, repoID int64, cfg Config, rc repoconfig.RepoConfig, files map[string]string) ([]Finding, []string, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)
	system := fillNits(fillInstructions(fillLanguage(promptReviewer, rc.Language), rc.Instructions), rc.Profile)

	var (
		mu        sync.Mutex
		findings  []Finding
		discarded []string
	)
	for file, hunks := range files {
		g.Go(func() error {
			related := relatedContext(ctx, retr, repoID, file, hunks, cfg.ReviewContextMaxChars)
			fs, err := reviewFile(ctx, gw, cfg.AgentRetries, system, file, hunks, related)
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

// relatedContext arma el bloque "Símbolos relacionados" del archivo (§6 F4).
// Best-effort por contrato (§9.6: la revisión jamás se degrada por indexar):
// sin retriever, índice vacío, sin símbolos o retrieval fallida → "" y el
// prompt queda byte-idéntico al de siempre. La query es el inicio de los
// hunks (ya topados upstream) — nunca los hunks completos.
func relatedContext(ctx context.Context, retr Retriever, repoID int64, file, hunks string, maxChars int) string {
	if retr == nil {
		return ""
	}
	query := hunks
	if len(query) > retrievalQueryMaxChars {
		query = query[:retrievalQueryMaxChars]
	}
	syms, err := retr.RetrieveRelated(ctx, repoID, file, query)
	if err != nil {
		slog.Warn("review: retrieval de símbolos falló, el archivo sigue sin contexto (§9.6)",
			"archivo", file, "error", err)
		return ""
	}
	return relatedBlock(syms, maxChars)
}

// relatedBlock renderiza el bloque de símbolos relacionados (§6 F4): una
// línea compacta por símbolo con tope de maxChars — se descartan entradas
// ENTERAS (nunca un corte a mitad de línea) y lo omitido queda declarado al
// pie. Sin símbolos o sin lugar ni para uno → "" (sin bloque).
func relatedBlock(syms []RelatedSymbol, maxChars int) string {
	n := len(syms)
	if n == 0 {
		return ""
	}
	// Presupuesto: header + líneas completas. Si hay entradas que van a
	// quedar fuera, se reserva el largo de la línea de omisión desde ya —
	// así el bloque final nunca excede el tope.
	total := len(relatedContextHeader)
	kept := 0
	for i, s := range syms {
		reserva := 0
		if rest := n - i - 1; rest > 0 {
			reserva = len(fmt.Sprintf("\n… (%d más omitidos)", rest))
		}
		line := symbolLine(s)
		if total+len(line)+1+reserva > maxChars {
			break
		}
		total += len(line) + 1
		kept++
	}
	if kept == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(relatedContextHeader)
	for _, s := range syms[:kept] {
		b.WriteString(symbolLine(s))
		b.WriteString("\n")
	}
	if omitted := n - kept; omitted > 0 {
		fmt.Fprintf(&b, "… (%d más omitidos)", omitted)
	}
	return b.String()
}

// symbolLine es la línea compacta de UN símbolo relacionado (§6 F4):
// archivo:línea kind símbolo (vía X).
func symbolLine(s RelatedSymbol) string {
	via := s.Via
	switch via {
	case "similar":
		via = "similitud"
	case "import":
		via = "import"
	}
	return fmt.Sprintf("%s:%d %s %s (vía %s)", s.File, s.StartLine, s.Kind, s.Symbol, via)
}

// reviewFile analiza UN archivo: llamada LLM, parseo estricto y reintentos
// por salida malformada (§9.8). El fallo del gateway (failover agotado) no
// se reintenta acá — el gateway ya agotó los suyos (§9.7). related es el
// bloque opcional de símbolos relacionados (§6 F4): vacío → prompt de
// siempre.
func reviewFile(ctx context.Context, gw Gateway, retries int, system, file, hunks, related string) ([]Finding, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		content, err := gw.Complete(ctx, roleReview, system, reviewerUser(file, hunks, related))
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
			if s.Mermaid != "" {
				clean, merr := cleanMermaid(s.Mermaid)
				if merr != nil {
					slog.Warn("review: mermaid del summarizer no parsea, se omite (§9.8)",
						"motivo", merr.Error())
					s.Mermaid = ""
				} else {
					s.Mermaid = clean // sin cerco de transporte: publication lo reenvuelve (§9.5)
				}
			}
			return s, nil
		}
		lastErr = perr
		slog.Warn("review: salida malformada del summarizer", "intento", attempt+1, "error", perr)
	}
	return nil, fmt.Errorf("salida malformada tras %d reintentos: %w", cfg.AgentRetries, lastErr)
}

// PreMergeStats es el input del agente Pre-merge (§6 F5): métricas ya
// computadas por el pipeline — el agente no recalcula nada, solo las
// interpreta en un veredicto advisory.
type PreMergeStats struct {
	RiskScore    int
	Counts       map[string]int // recuento por severidad del conjunto publicado
	Categories   []string       // categorías presentes en el conjunto publicado
	Coverage     []string       // declaraciones de cobertura (vacío = completa)
	Status       string         // estado que llevará la corrida (success|partial)
	Files        int            // archivos del diff
	ChangedLines int            // líneas cambiadas del diff
	TestFiles    int            // archivos de prueba dentro del diff
}

// PreMergeCheck es un ítem de la checklist del veredicto (§6 F5).
type PreMergeCheck struct {
	Item string
	OK   bool
}

// PreMergeResult es la salida validada del agente Pre-merge (§6 F5): texto
// advisory para la sección del resumen — jamás una acción del VCS (§1.1).
type PreMergeResult struct {
	Verdict   string
	Checklist []PreMergeCheck
	Resumen   string
}

// validPreMergeVerdict es el conjunto cerrado del veredicto (§6 F5): valores
// fijos en español, machine-parsed — el prompt los fija y acá se valida.
var validPreMergeVerdict = map[string]bool{
	"apto": true, "apto_con_observaciones": true, "no_apto": true,
}

// rawPreMerge es la salida cruda del Pre-merge antes de validar (§9.8).
type rawPreMerge struct {
	Verdict   string `json:"verdict"`
	Checklist []struct {
		Item string `json:"item"`
		OK   bool   `json:"ok"`
	} `json:"checklist"`
	Resumen string `json:"resumen"`
}

// runPreMerge genera el veredicto advisory pre-merge (§6 F5, rol review):
// UNA llamada con las métricas de la corrida, reintentos por salida
// malformada (§9.8). El fallo del gateway (failover agotado) no se reintenta
// acá — el gateway ya agotó los suyos (§9.7).
func runPreMerge(ctx context.Context, gw Gateway, cfg Config, rc repoconfig.RepoConfig, stats PreMergeStats) (*PreMergeResult, error) {
	system := fillLanguage(promptPremerge, rc.Language)
	var lastErr error
	for attempt := 0; attempt <= cfg.AgentRetries; attempt++ {
		content, err := gw.Complete(ctx, roleReview, system, premergeUser(stats))
		if err != nil {
			return nil, fmt.Errorf("failover agotado: %w", err)
		}
		pm, perr := parsePreMerge(content)
		if perr == nil {
			return pm, nil
		}
		lastErr = perr
		slog.Warn("review: salida malformada del pre-merge", "intento", attempt+1, "error", perr)
	}
	return nil, fmt.Errorf("salida malformada tras %d reintentos: %w", cfg.AgentRetries, lastErr)
}

// premergeUser arma el prompt de usuario del Pre-merge: estado de la
// corrida, risk score, recuentos del conjunto publicado, categorías,
// estadísticas del diff y la declaración de cobertura — todo dato ya
// computado por el pipeline (§6 F5).
func premergeUser(stats PreMergeStats) string {
	var b strings.Builder
	b.WriteString(premergeMarker + "\n\n")
	fmt.Fprintf(&b, "Estado de la corrida: %s.\n", stats.Status)
	fmt.Fprintf(&b, "Risk score computado: %d/100.\n", stats.RiskScore)
	fmt.Fprintf(&b, "Hallazgos publicados: %d alta, %d media, %d baja.\n",
		stats.Counts["high"], stats.Counts["medium"], stats.Counts["low"])
	cats := "ninguna"
	if len(stats.Categories) > 0 {
		cats = strings.Join(stats.Categories, ", ")
	}
	fmt.Fprintf(&b, "Categorías de los hallazgos: %s.\n", cats)
	fmt.Fprintf(&b, "Diff: %d líneas cambiadas en %d archivos (%d de pruebas).\n",
		stats.ChangedLines, stats.Files, stats.TestFiles)
	if len(stats.Coverage) > 0 {
		b.WriteString("Cobertura parcial declarada:\n")
		for _, c := range stats.Coverage {
			b.WriteString("- " + c + "\n")
		}
	} else {
		b.WriteString("Cobertura completa: SAST + LLM sobre todo el diff.\n")
	}
	return b.String()
}

// parsePreMerge valida la salida JSON del Pre-merge (§9.8): veredicto en el
// conjunto cerrado, resumen obligatorio y checklist de 0 a 8 ítems con texto
// no vacío. El prompt pide 3-6; el validador tolera el borde para no quemar
// reintentos por una checklist corta pero honesta.
func parsePreMerge(content string) (*PreMergeResult, error) {
	var raw rawPreMerge
	if err := json.Unmarshal([]byte(stripFences(content)), &raw); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if !validPreMergeVerdict[raw.Verdict] {
		return nil, fmt.Errorf("verdict %q fuera del conjunto cerrado", raw.Verdict)
	}
	if strings.TrimSpace(raw.Resumen) == "" {
		return nil, fmt.Errorf("resumen vacío")
	}
	if len(raw.Checklist) > 8 {
		return nil, fmt.Errorf("checklist con %d ítems, tope 8", len(raw.Checklist))
	}
	out := &PreMergeResult{Verdict: raw.Verdict, Resumen: raw.Resumen}
	for i, c := range raw.Checklist {
		if strings.TrimSpace(c.Item) == "" {
			return nil, fmt.Errorf("checklist %d: item vacío", i)
		}
		out.Checklist = append(out.Checklist, PreMergeCheck{Item: c.Item, OK: c.OK})
	}
	return out, nil
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

// rawVerdict es la salida cruda del Verifier para UN hallazgo (contrato
// §9.8): un elemento por hallazgo de entrada, mismo index.
type rawVerdict struct {
	Index    int    `json:"index"`
	Verified bool   `json:"verified"`
	Reason   string `json:"reason"`
}

// runVerifier verifica los hallazgos LLM del conjunto publicado (§3.3/§6 F3,
// rol cheap): una llamada por archivo con hallazgos a publicar, los
// hallazgos SAST del mismo archivo como referencia y sus hunks como
// evidencia. Devuelve el veredicto por huella — solo de los archivos
// verificados: los descartados (§9.8) quedan sin veredicto — y la lista de
// descartes para el registro. Error solo si el contexto se cancela.
func runVerifier(ctx context.Context, gw Gateway, cfg Config, fileHunks map[string]string, sastByFile map[string][]Finding, findings []Finding) (map[string]bool, []string, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)

	byFile := map[string][]Finding{}
	for _, f := range findings {
		byFile[f.File] = append(byFile[f.File], f)
	}

	var (
		mu        sync.Mutex
		verdicts  = map[string]bool{}
		discarded []string
	)
	for file, fs := range byFile {
		g.Go(func() error {
			v, err := verifyFile(ctx, gw, cfg.AgentRetries, file, fileHunks[file], sastByFile[file], fs)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				discarded = append(discarded, fmt.Sprintf("%s (%v)", file, err))
				return nil // el descarte no aborta la corrida (§9.8)
			}
			for i, f := range fs {
				if verified, ok := v[i]; ok {
					verdicts[Fingerprint(f.File, f.Category, f.Anchor)] = verified
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, fmt.Errorf("esperando la verificación del verifier: %w", err)
	}
	sort.Strings(discarded)
	return verdicts, discarded, nil
}

// verifyFile verifica UN archivo: llamada al rol cheap, parseo estricto y
// reintentos por salida malformada (§9.8). El fallo del gateway (failover
// agotado) no se reintenta acá — el gateway ya agotó los suyos (§9.7).
func verifyFile(ctx context.Context, gw Gateway, retries int, file, hunks string, sast, fs []Finding) (map[int]bool, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		content, err := gw.Complete(ctx, roleCheap, promptVerifier, verifierUser(file, hunks, fs, sast))
		if err != nil {
			return nil, fmt.Errorf("failover agotado: %w", err)
		}
		v, perr := parseVerdicts(content, len(fs))
		if perr == nil {
			return v, nil
		}
		lastErr = perr
		slog.Warn("review: salida malformada del verifier",
			"archivo", file, "intento", attempt+1, "error", perr)
	}
	return nil, fmt.Errorf("salida malformada tras %d reintentos: %w", retries, lastErr)
}

// parseVerdicts valida la salida JSON del Verifier contra los n hallazgos de
// entrada. Estricta como parseFindings (§9.8): exactamente un veredicto por
// hallazgo, índices en rango y sin duplicados — cualquier desvío invalida
// toda la salida → reintento.
func parseVerdicts(content string, n int) (map[int]bool, error) {
	var raw []rawVerdict
	if err := json.Unmarshal([]byte(stripFences(content)), &raw); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if len(raw) != n {
		return nil, fmt.Errorf("esperaba %d veredictos, llegaron %d", n, len(raw))
	}
	out := make(map[int]bool, n)
	for _, r := range raw {
		switch {
		case r.Index < 0 || r.Index >= n:
			return nil, fmt.Errorf("veredicto %d: índice %d fuera de rango", r.Index, r.Index)
		case strings.TrimSpace(r.Reason) == "":
			return nil, fmt.Errorf("veredicto %d: reason vacío", r.Index)
		}
		if _, dup := out[r.Index]; dup {
			return nil, fmt.Errorf("índice duplicado %d", r.Index)
		}
		out[r.Index] = r.Verified
	}
	return out, nil
}

// verifyFindingsJSON serializa el lote de un archivo para el prompt del
// Verifier: índice explícito por hallazgo — la respuesta se mapea por él.
func verifyFindingsJSON(fs []Finding) string {
	var b strings.Builder
	b.WriteString("[")
	for i, f := range fs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n  {\"index\": %d, \"severity\": %q, \"category\": %q, \"line\": %d, \"body\": %q}",
			i, f.Severity, f.Category, f.Line, f.Body)
	}
	b.WriteString("\n]")
	return b.String()
}

// verifierUser arma el prompt de usuario del Verifier: los hallazgos a
// verificar (indexados), los SAST del mismo archivo como referencia — su
// ausencia NO es evidencia de falso positivo (§9.4) — y los hunks del diff
// como evidencia.
func verifierUser(file, hunks string, fs, sast []Finding) string {
	var b strings.Builder
	b.WriteString(verifierUserMarker + "\n\n")
	fmt.Fprintf(&b, "Archivo: %s\n\n", file)
	b.WriteString("Hallazgos a verificar:\n```json\n" + verifyFindingsJSON(fs) + "\n```\n\n")
	b.WriteString("Hallazgos SAST del mismo archivo (referencia; su ausencia NO es evidencia de falso positivo):\n```json\n")
	if len(sast) == 0 {
		b.WriteString("[]")
	} else {
		b.WriteString(verifyFindingsJSON(sast))
	}
	fmt.Fprintf(&b, "\n```\n\nHunks del diff de %s:\n\n```diff\n%s```", file, hunks)
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

// reviewerUser arma el prompt de usuario del Reviewer: archivo + sus hunks
// completos (encabezados incluidos — el contexto ayuda al anclaje) y, si el
// retriever aportó símbolos del índice, el bloque "Símbolos relacionados"
// después del fence del diff (§6 F4). related vacío → prompt byte-idéntico
// al de siempre (compatible con repos sin indexar).
func reviewerUser(file, hunks, related string) string {
	return userFilePrefix + file + "\n\n```diff\n" + hunks + "```" + related
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
