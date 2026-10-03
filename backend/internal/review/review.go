// Package review orquesta el pipeline de revisión (mapa: backend.review):
// diff → SAST → Reviewer LLM por archivo → dedup por huella → Summarizer →
// publicación en dos fases (§3.6). Llama a llm.Complete, analyze.Run y
// vcs.Post*: no conoce HTTP ni webhooks (§3.5) — el ReviewJob del worker es
// su único caller. El loop de agente es código propio, sin framework.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	"github.com/Andres39128/codeowl/backend/prompts"
)

// Estados del conjunto cerrado de reviews (§3.3). running vive solo en la
// fila entre CreateReview y la actualización final.
const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusPartial = "partial"
	StatusStale   = "stale"
	StatusFailed  = "failed"
)

// Fuentes del hallazgo (§3.3): source de findings.
const (
	SourceLLM  = "llm"
	SourceSAST = "sast"
)

// Defaults espejo de config.Default* (§4.2: el pipeline no importa config —
// el wiring del worker llena Config desde Stage2Config; estos valores cubren
// el zero-value y los tests).
const (
	defaultDiffMaxLines     = 4000
	defaultDiffFileMaxLines = 1000
	defaultAgentRetries     = 2
	defaultDriftLines       = 3
	defaultCacheTTL         = time.Hour
	defaultCacheMaxEntries  = 100
	defaultConcurrency      = 4
	defaultReviewProfile    = "assertive" // §9.5: perfil global si el repo no define uno
)

// Store es el subconjunto de la store que consume el pipeline (§4.5:
// interfaces donde se consumen). *store.Store la satisface.
type Store interface {
	GetRepository(ctx context.Context, id int64) (store.Repository, error)
	GetPullRequest(ctx context.Context, id int64) (store.PullRequest, error)
	CreateReview(ctx context.Context, arg store.CreateReviewParams) (store.Review, error)
	GetLatestReviewByPR(ctx context.Context, pullRequestID int64) (store.Review, error)
	ListFindingsByReview(ctx context.Context, reviewID int64) ([]store.Finding, error)
	UpdateReviewStatus(ctx context.Context, arg store.UpdateReviewStatusParams) (store.Review, error)
	UpdateReviewSummary(ctx context.Context, arg store.UpdateReviewSummaryParams) (store.Review, error)
	CreateFinding(ctx context.Context, arg store.CreateFindingParams) (store.Finding, error)
	GetCommentsSentByPRAndType(ctx context.Context, arg store.GetCommentsSentByPRAndTypeParams) ([]store.CommentsSent, error)
	CreateCommentSent(ctx context.Context, arg store.CreateCommentSentParams) (store.CommentsSent, error)
	UpdateCommentSentCommentID(ctx context.Context, arg store.UpdateCommentSentCommentIDParams) (store.CommentsSent, error)
}

// Gateway es lo único que el pipeline le pide al LLM (mapa: los agentes
// Reviewer/Summarizer consumen el rol review). *llm.Gateway la satisface.
type Gateway interface {
	Complete(ctx context.Context, role, systemPrompt, userPrompt string) (string, error)
}

// Analyzer es el cliente del sandbox SAST (§9.4). *analyze.Runner la satisface.
type Analyzer interface {
	Run(ctx context.Context, workdir string) (*analyze.AnalysisResult, error)
}

// Config son los topes del pipeline — todos config (§9.6/§9.8). El wiring
// del worker los llena desde config.Stage2Config.
type Config struct {
	DiffMaxLines     int           // diff sobre el tope → solo resumen (§9.6)
	DiffFileMaxLines int           // archivo sobre el tope → solo SAST (§9.6)
	AgentRetries     int           // reintentos por salida malformada (§9.8)
	DriftLines       int           // tolerancia de drift del ancla de dedup (§3.6.3)
	CacheTTL         time.Duration // TTL de la cache de resultados (§9.6)
	CacheMaxEntries  int           // tope de entradas de la cache (§9.6)
	Concurrency      int           // análisis de archivos en paralelo (tope gateway por review, §9.6)
	DefaultProfile   string        // perfil global para repos sin review.yaml (§9.5: chill|assertive|strict)
}

// DefaultConfig devuelve la config por defecto (espejo de los defaults de
// config.Stage2Config — §4.2).
func DefaultConfig() Config {
	return Config{
		DiffMaxLines:     defaultDiffMaxLines,
		DiffFileMaxLines: defaultDiffFileMaxLines,
		AgentRetries:     defaultAgentRetries,
		DriftLines:       defaultDriftLines,
		CacheTTL:         defaultCacheTTL,
		CacheMaxEntries:  defaultCacheMaxEntries,
		Concurrency:      defaultConcurrency,
	}
}

// normalized clampea los valores fuera de rango a los defaults: la config
// inválida nunca cuelga el pipeline (mismo criterio que llm.New).
func (c Config) normalized() Config {
	d := DefaultConfig()
	if c.DiffMaxLines < 1 {
		c.DiffMaxLines = d.DiffMaxLines
	}
	if c.DiffFileMaxLines < 1 {
		c.DiffFileMaxLines = d.DiffFileMaxLines
	}
	if c.AgentRetries < 0 {
		c.AgentRetries = d.AgentRetries
	}
	if c.DriftLines < 0 {
		c.DriftLines = d.DriftLines
	}
	if c.CacheTTL <= 0 {
		c.CacheTTL = d.CacheTTL
	}
	if c.CacheMaxEntries < 1 {
		c.CacheMaxEntries = d.CacheMaxEntries
	}
	if c.Concurrency < 1 {
		c.Concurrency = d.Concurrency
	}
	if !validProfiles[c.DefaultProfile] {
		c.DefaultProfile = defaultReviewProfile // inválida o cero-value → default del pipeline
	}
	return c
}

// ReviewInput es la identidad de la corrida (§3.6.1): el job guarda
// (head_sha, base_sha) del evento que la disparó.
type ReviewInput struct {
	PullRequestID int64
	RepositoryID  int64
	HeadSHA       string
	BaseSHA       string
	Workdir       string // path del clon shallow del job
}

// ReviewResult es el desenlace de la corrida para el caller (job/API).
type ReviewResult struct {
	Status        string // success, partial, stale, failed
	Summary       string
	Walkthrough   string
	Mermaid       string
	FindingsCount int
}

// Finding es el hallazgo unificado del pipeline — de LLM o SAST — antes de
// persistir y publicar (§3.3).
type Finding struct {
	File       string
	Line       int32
	Severity   string // high | medium | low (§3.3, conjunto cerrado)
	Category   string // security | logic | performance | style | tests | other
	Body       string
	Suggestion string // opcional, bloque ```suggestion``` en el comentario
	Source     string // llm | sast
}

// Run ejecuta el pipeline completo para una corrida (§3.6): diff → SAST →
// Reviewer → dedup → Summarizer → publicación en dos fases → re-edición del
// resumen con el recuento final. Devuelve error solo ante fallos de
// infraestructura reintentables (diff, BD) — el job reintenta y Run reusa la
// fila running — o ante fallo de publicación, que ya marcó la corrida
// failed (§9.7). Los recortes de cobertura NO son error: quedan partial y
// declarados en el resumen (§9.6 — jamás recorte silencioso).
func Run(ctx context.Context, cfg Config, st Store, gw Gateway, analyzer Analyzer, provider vcs.VCSProvider, input ReviewInput) (*ReviewResult, error) {
	cfg = cfg.normalized()
	results.configure(cfg.CacheTTL, cfg.CacheMaxEntries)

	repo, err := st.GetRepository(ctx, input.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("cargando el repo %d: %w", input.RepositoryID, err)
	}
	pr, err := st.GetPullRequest(ctx, input.PullRequestID)
	if err != nil {
		return nil, fmt.Errorf("cargando el PR %d: %w", input.PullRequestID, err)
	}

	// (a) Fila de la corrida. Reuso idempotente: un reintento del job
	// encuentra su propia fila running con la misma identidad y la continúa
	// en vez de acumular filas (§9.7: reintentos del job).
	rev, err := findOrCreateReview(ctx, st, input)
	if err != nil {
		return nil, err
	}
	// Los slots de concurrencia por review del gateway (§9.6) se anclan al
	// ID de la corrida.
	ctx = llm.WithReview(ctx, strconv.FormatInt(rev.ID, 10))

	res := &ReviewResult{Status: StatusRunning}

	// (b) Chequeo stale al iniciar (§3.6.1.3): identidad distinta o PR
	// cerrado → stale sin gastar LLM ni diff.
	if isStale(pr, input) {
		return markStale(ctx, st, rev.ID, res)
	}

	// (c) Diff unificado base...head (tres puntos: contra el merge-base).
	diff, err := provider.GetDiff(ctx, &repo, &pr)
	if err != nil {
		return nil, fmt.Errorf("obteniendo el diff del PR %d: %w", pr.Number, err)
	}

	var coverage []string // declaraciones de cobertura para el resumen (§9.6)

	// (d) SAST sobre el clon (§9.4). Un fallo del sandbox es un gap de
	// cobertura declarado — jamás ausencia silenciosa de hallazgos.
	var sast []Finding
	sastRes, err := analyzer.Run(ctx, input.Workdir)
	if err != nil {
		slog.Warn("review: analyzer falló, la corrida sigue sin SAST", "error", err)
		coverage = append(coverage, "análisis SAST omitido: el analyzer falló ("+err.Error()+")")
	} else {
		sast = fromSAST(sastRes.Findings)
	}

	// Topes de contenido (§9.6): diff sobre el tope → solo resumen (sin LLM
	// ni inline); archivo sobre el tope → ese archivo solo SAST.
	fileHunks, changed, total := splitDiffByFile(diff)
	overCap := total > cfg.DiffMaxLines
	if overCap {
		coverage = append(coverage, fmt.Sprintf(
			"diff de %d líneas sobre el tope de %d: sin análisis LLM ni comentarios inline, solo resumen",
			total, cfg.DiffMaxLines))
	} else {
		for file, n := range changed {
			if n > cfg.DiffFileMaxLines {
				delete(fileHunks, file)
				coverage = append(coverage, fmt.Sprintf(
					"archivo %s con %d líneas sobre el tope de %d: solo SAST", file, n, cfg.DiffFileMaxLines))
			}
		}
	}

	// Merge-base y config efectiva del repo (§9.5): review.yaml se lee del
	// MERGE-BASE, no del head ni del tip de la base — leerlo del head dejaría
	// al PR escribir sus propias reglas de revisión (inyección). El merge-base
	// se resuelve una vez y ancla también la clave de cache (§9.6).
	mb := mergeBase(ctx, input.Workdir, input.BaseSHA, input.HeadSHA)
	rc := loadRepoConfig(ctx, input.Workdir, mb, repo, cfg.DefaultProfile)

	// path_filters (§9.5): delimitan qué examina la corrida. Los archivos
	// fuera de los filtros no llegan al LLM (ahorro de costo) ni generan
	// hallazgos de SAST — el analyzer corre UNA vez sobre el clon completo y
	// el filtro opera en este mismo punto de selección. Filtrar todo no es
	// error: la corrida sigue y completa con cero hallazgos.
	dropped := 0
	for file := range fileHunks {
		if !matchPath(rc.PathFilters, file) {
			delete(fileHunks, file)
			dropped++
		}
	}
	kept := sast[:0]
	for _, f := range sast {
		if matchPath(rc.PathFilters, f.File) {
			kept = append(kept, f)
		}
	}
	sast = kept
	if dropped > 0 {
		slog.Info("review: archivos excluidos por path_filters", "cantidad", dropped, "review", rev.ID)
	}

	// (e)(f) Reviewer LLM por archivo, con cache de resultados (§9.6).
	var llmFindings []Finding
	var sum *SummaryResult
	cacheable := !overCap && len(fileHunks) > 0
	if entry, ok := results.get(cacheKey(input, mb, cfg, rc)); ok {
		llmFindings, sum = entry.findings, entry.summary
		slog.Info("review: cache hit", "review", rev.ID)
	} else if !overCap && len(fileHunks) > 0 {
		var discarded []string
		llmFindings, discarded, err = runReviewer(ctx, gw, cfg, rc, fileHunks)
		if err != nil {
			return nil, err // cancelación del contexto: el job reintenta
		}
		for _, d := range discarded {
			coverage = append(coverage, "análisis LLM omitido: "+d)
		}
		cacheable = cacheable && len(discarded) == 0
	}

	// Dedup: dentro de la corrida por huella exacta y contra comments_sent
	// con tolerancia de drift (§3.6.3) — un push que corre líneas no
	// republica el mismo hallazgo.
	existing, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: input.PullRequestID,
		Type:          "inline",
	})
	if err != nil {
		return nil, fmt.Errorf("cargando comments_sent del PR: %w", err)
	}
	publishable := dedup(existing, append(llmFindings, sast...), cfg.DriftLines)

	// Filtro de publicación del perfil (§6 F3): el perfil regula CUÁNTO
	// comenta el bot, no qué se registra — todo hallazgo persiste (auditoría)
	// y solo el conjunto publicado alimenta los inline, el recuento del
	// resumen y FindingsCount. Jamás produce veredicto bloqueante ni cambia
	// el estado de la corrida (§1.1).
	var toPublish []Finding
	for _, f := range publishable {
		if isPublishable(rc.Profile, f) {
			toPublish = append(toPublish, f)
		}
	}

	// Persistencia de los hallazgos de esta corrida (verified null hasta el
	// Verifier de F3, §3.3).
	for _, f := range publishable {
		if _, err := st.CreateFinding(ctx, store.CreateFindingParams{
			ReviewID:   rev.ID,
			File:       f.File,
			Line:       f.Line,
			Severity:   f.Severity,
			Category:   f.Category,
			Body:       f.Body,
			Suggestion: pgText(f.Suggestion),
			Source:     f.Source,
		}); err != nil {
			return nil, fmt.Errorf("persistiendo el finding de %s: %w", f.File, err)
		}
	}

	// (g) Summarizer: estadísticas del diff + recuento de hallazgos
	// publicados (el resumen refleja lo que el bot comenta, §6 F3).
	counts := countBySeverity(toPublish)
	if sum == nil {
		if sum, err = runSummarizer(ctx, gw, cfg, rc.Language, diffStats(total, changed), counts); err != nil {
			// Failover agotado en el summarizer: la corrida queda partial y
			// lo declara — el resumen provisional no puede faltar (§9.6).
			slog.Warn("review: summarizer falló, resumen de reserva", "error", err)
			coverage = append(coverage, "resumen del LLM omitido: failover agotado")
			sum = &SummaryResult{Summary: "La revisión corrió pero el resumen del LLM no pudo generarse (proveedores del rol review sin respuesta)."}
			cacheable = false
		}
	}
	res.Summary, res.Walkthrough, res.Mermaid = sum.Summary, sum.Walkthrough, sum.Mermaid

	// Re-chequeo stale pre-publicación (§3.6.1.3): incluye el cierre del PR
	// — comentar un PR cerrado no aporta y quema cuota del VCS.
	if pr, err = st.GetPullRequest(ctx, input.PullRequestID); err != nil {
		return nil, fmt.Errorf("re-chequeo del PR antes de publicar: %w", err)
	}
	if isStale(pr, input) {
		return markStale(ctx, st, rev.ID, res)
	}

	// (h) Fase 1 de la publicación: resumen provisional edit-in-place.
	pub, err := newPublisher(ctx, provider, &repo, &pr, st, input.PullRequestID, rev.ID)
	if err != nil {
		return nil, fmt.Errorf("cargando el resumen previo del PR: %w", err)
	}
	if err := pub.publishSummary(ctx, provisionalSummary(sum)); err != nil {
		return failRun(ctx, st, rev.ID, res, fmt.Errorf("publicando el resumen: %w", err), nil)
	}

	// (i) Fase 2: comentarios inline. La línea fuera del diff visible baja
	// al resumen (§3.6.5); con diff sobre el tope todo baja al resumen. Solo
	// publica el conjunto del perfil (§6 F3).
	var outOfDiff []Finding
	for _, f := range toPublish {
		if overCap {
			outOfDiff = append(outOfDiff, f)
			continue
		}
		pos, ok := vcs.MapFindingToPosition(diff, f.File, f.Line)
		if !ok {
			outOfDiff = append(outOfDiff, f)
			continue
		}
		if err := pub.publishInline(ctx, f, pos); err != nil {
			// (§9.7) La corrida queda failed y el resumen se re-edita mejor
			// esfuerzo con el estado final. El reintento del job republica
			// solo lo que falta: lo ya publicado deduplica por huella.
			return failRun(ctx, st, rev.ID, res, fmt.Errorf("publicando el comentario inline de %s: %w", f.File, err),
				func() {
					body := finalSummary(sum, counts, append(outOfDiff, f), coverage, err.Error())
					if sumErr := pub.publishSummary(ctx, body); sumErr != nil {
						slog.Warn("review: re-edición del resumen en fallo también falló", "error", sumErr)
					}
				})
		}
	}
	res.FindingsCount = len(toPublish)

	// (j)(k) Estado final y re-edición del resumen (§3.6.2): el de la fase 1
	// es provisional por diseño — este incluye el recuento por severidad, los
	// hallazgos fuera del diff y la declaración de cobertura (§9.6).
	status := StatusSuccess
	if len(coverage) > 0 {
		status = StatusPartial
	}
	body := finalSummary(sum, counts, outOfDiff, coverage, "")
	if err := pub.publishSummary(ctx, body); err != nil {
		// El resumen de fase 1 ya está publicado: la re-edición fallida no
		// invalida la corrida — queda el provisional sin el recuento final.
		slog.Warn("review: re-edición final del resumen falló", "error", err)
		status = StatusPartial
	}
	if _, err := st.UpdateReviewSummary(ctx, store.UpdateReviewSummaryParams{
		ID:          rev.ID,
		Summary:     sum.Summary,
		Walkthrough: sum.Walkthrough,
		Mermaid:     sum.Mermaid,
	}); err != nil {
		return nil, fmt.Errorf("actualizando la review %d: %w", rev.ID, err)
	}
	if _, err := st.UpdateReviewStatus(ctx, store.UpdateReviewStatusParams{ID: rev.ID, Status: status}); err != nil {
		return nil, fmt.Errorf("marcando la review %d como %s: %w", rev.ID, status, err)
	}

	res.Status = status
	if cacheable {
		results.put(cacheKey(input, mb, cfg, rc), cacheEntry{findings: llmFindings, summary: sum})
	}
	return res, nil
}

// isStale compara la identidad de la corrida contra el estado actual del PR
// (§3.6.1.3): push o retarget la reemplazan; un PR cerrado no publica.
func isStale(pr store.PullRequest, input ReviewInput) bool {
	return pr.HeadSha != input.HeadSHA ||
		pr.BaseSha != input.BaseSHA ||
		pr.State != "open"
}

// markStale marca la corrida stale y corta sin publicar (§3.6.1.3).
func markStale(ctx context.Context, st Store, reviewID int64, res *ReviewResult) (*ReviewResult, error) {
	if _, err := st.UpdateReviewStatus(ctx, store.UpdateReviewStatusParams{ID: reviewID, Status: StatusStale}); err != nil {
		return nil, fmt.Errorf("marcando la review %d como stale: %w", reviewID, err)
	}
	res.Status = StatusStale
	slog.Info("review: corrida stale, sin LLM ni publicación", "review", reviewID)
	return res, nil
}

// findOrCreateReview devuelve la fila running de esta identidad (reintento
// del job, §9.7) o crea una nueva. Nace running con textos vacíos (§3.3).
func findOrCreateReview(ctx context.Context, st Store, input ReviewInput) (store.Review, error) {
	latest, err := st.GetLatestReviewByPR(ctx, input.PullRequestID)
	switch {
	case err == nil &&
		latest.Status == StatusRunning &&
		latest.HeadSha == input.HeadSHA &&
		latest.BaseSha == input.BaseSHA:
		return latest, nil
	case err == nil || errors.Is(err, pgx.ErrNoRows):
		rev, cerr := st.CreateReview(ctx, store.CreateReviewParams{
			PullRequestID: input.PullRequestID,
			HeadSha:       input.HeadSHA,
			BaseSha:       input.BaseSHA,
		})
		if cerr != nil {
			return store.Review{}, fmt.Errorf("creando la review del PR %d: %w", input.PullRequestID, cerr)
		}
		return rev, nil
	default:
		return store.Review{}, fmt.Errorf("buscando la última review del PR %d: %w", input.PullRequestID, err)
	}
}

// failRun marca la corrida failed (§9.7), ejecuta el re-edit best-effort que
// el caller provea y devuelve el error para que el job decida su reintento.
func failRun(ctx context.Context, st Store, reviewID int64, res *ReviewResult, cause error, reedit func()) (*ReviewResult, error) {
	if _, err := st.UpdateReviewStatus(ctx, store.UpdateReviewStatusParams{ID: reviewID, Status: StatusFailed}); err != nil {
		slog.Warn("review: marcando failed también falló", "review", reviewID, "error", err)
	}
	if reedit != nil {
		reedit()
	}
	res.Status = StatusFailed
	return res, cause
}

// fromSAST convierte los hallazgos normalizados del analyzer (§3.3) al tipo
// unificado del pipeline.
func fromSAST(fs []analyze.Finding) []Finding {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		body := f.Body
		if f.Linter != "" {
			body = fmt.Sprintf("%s (regla: %s/%s)", body, f.Linter, f.Rule)
		}
		out = append(out, Finding{
			File:     f.File,
			Line:     int32(f.Line),
			Severity: f.Severity,
			Category: f.Category,
			Body:     body,
			Source:   SourceSAST,
		})
	}
	return out
}

// dedup filtra hallazgos: duplicados exactos dentro de la corrida (por
// huella) y hallazgos ya comentados en runs anteriores (misma huella con
// drift de líneas, §3.6.3). Devuelve en orden estable.
func dedup(existing []store.CommentsSent, findings []Finding, drift int) []Finding {
	seen := make(map[string]bool, len(findings))
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		fp := Fingerprint(f.File, f.Category, f.Line)
		if seen[fp] || IsDuplicate(existing, f, drift) {
			continue
		}
		seen[fp] = true
		out = append(out, f)
	}
	sortFindings(out)
	return out
}

// countBySeverity cuenta hallazgos por severidad (recuento del resumen,
// §3.6.2).
func countBySeverity(fs []Finding) map[string]int {
	counts := map[string]int{"high": 0, "medium": 0, "low": 0}
	for _, f := range fs {
		counts[f.Severity]++
	}
	return counts
}

// diffStats arma el texto de estadísticas que consume el Summarizer.
func diffStats(total int, changed map[string]int) string {
	files := make([]string, 0, len(changed))
	for f := range changed {
		files = append(files, f)
	}
	sortStrings(files)
	var b strings.Builder
	fmt.Fprintf(&b, "Cambios: %d líneas en %d archivos.", total, len(changed))
	for _, f := range files {
		fmt.Fprintf(&b, "\n- %s: %d líneas cambiadas", f, changed[f])
	}
	return b.String()
}

// cacheKey es la clave de la cache de resultados (§9.6): head SHA +
// merge-base + versión del prompt + hash de la config efectiva — los topes
// del pipeline Y la config del repo (§9.5: dos configs distintas son
// corridas distintas). El merge-base lo resuelve el caller una sola vez (Run
// ya lo necesitó para leer review.yaml) y, si git no pudo, cayó al base_sha
// — la cache es una optimización, no una promesa.
func cacheKey(input ReviewInput, mb string, cfg Config, rc RepoConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "%+v", cfg) // topes normalizados: configs distintas son corridas distintas
	fmt.Fprintf(h, "%+v", rc)  // config efectiva del repo: language/profile/filtros/instrucciones (§9.5)
	return input.HeadSHA + "|" + mb + "|" + prompts.Version() + "|" + hex.EncodeToString(h.Sum(nil))
}

// mergeBase resuelve el merge-base con git en el clon del job. Sin timeout
// propio: el ctx del job lo acota (comando local, milisegundos).
func mergeBase(ctx context.Context, workdir, base, head string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", workdir, "merge-base", base, head).Output()
	if err != nil {
		slog.Warn("review: merge-base falló, la cache ancla en base_sha", "error", err)
		return base
	}
	return strings.TrimSpace(string(out))
}

// cacheEntry es lo que la cache guarda: la salida de los agentes LLM para un
// diff dado (§9.6 — el SAST es determinístico y no se cachea).
type cacheEntry struct {
	findings []Finding
	summary  *SummaryResult
	savedAt  time.Time
}

// resultCache es la cache en memoria del worker (§9.6): single worker por
// diseño — no justifica tabla ni servicio; se pierde al reiniciar y un miss
// solo recomputa.
type resultCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]cacheEntry
}

// results es la cache del proceso. TTL y tope se fijan por corrida (la
// config es estable en la vida del worker).
var results = &resultCache{entries: map[string]cacheEntry{}}

func (c *resultCache) configure(ttl time.Duration, max int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl, c.max = ttl, max
}

func (c *resultCache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	if c.ttl > 0 && time.Since(e.savedAt) > c.ttl {
		delete(c.entries, key)
		return cacheEntry{}, false
	}
	return e, true
}

func (c *resultCache) put(key string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max < 1 {
		c.max = defaultCacheMaxEntries
	}
	if c.ttl <= 0 {
		c.ttl = defaultCacheTTL
	}
	e.savedAt = time.Now()
	// Tope de entradas: al llenar, expira lo vencido y si aún no entra,
	// desaloja una entrada arbitraria (map iteration — suficiente para una
	// cache que pierde al reiniciar).
	for len(c.entries) >= c.max {
		for k, old := range c.entries {
			if c.ttl > 0 && time.Since(old.savedAt) > c.ttl {
				delete(c.entries, k)
				continue
			}
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = e
}

// pgText convierte un string opcional a pgtype.Text.
func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
