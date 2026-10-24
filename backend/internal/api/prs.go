// Lectura de PRs para el dashboard (mapa: rest_endpoints — F3, guía §6
// "detalle de PR con findings y diff"). Solo lectura y visible para cualquier
// usuario autenticado (§3.4: el member opera el triage y consulta PRs) —
// auth sin CSRF, como el panel de cola. El diff baja bajo demanda vía
// GetDiff del adapter del repo (mapa: backend.api.nota — la api depende de
// vcs para GetDiff); la api jamás ejecuta análisis (§3.5).
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// prRepoView es el repo embebido del PR: lo mínimo para mostrar el origen.
type prRepoView struct {
	ID    int64  `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Vcs   string `json:"vcs"`
}

// reviewCounts son los hallazgos de la última corrida, por severidad
// (conjunto cerrado del schema, §3.3: high/medium/low).
type reviewCounts struct {
	High   int64 `json:"high"`
	Medium int64 `json:"medium"`
	Low    int64 `json:"low"`
}

// latestReviewView es la corrida más reciente del PR, versión lista.
type latestReviewView struct {
	ID        int64        `json:"id"`
	Status    string       `json:"status"`
	CreatedAt time.Time    `json:"created_at"`
	Counts    reviewCounts `json:"counts"`
}

// prView es la fila del listado y el bloque "pr" del detalle. Sin title:
// pull_requests no la persiste (solo author/number — la guía §3.3 guarda lo
// mínimo; si el dashboard quiere título, es trabajo del VCS, no de la BD).
// RiskScore es el proxy del tail de Run (§6 F5): nil = sin corrida que lo
// haya calculado (columna NULL en la BD). EffortScore/ImpactScore son el
// triage extendido (§6 F6): effort siempre computable del diff, impact
// null sin índice del repo (degradación visible).
type prView struct {
	ID           int64             `json:"id"`
	Number       int64             `json:"number"`
	Author       string            `json:"author"`
	State        string            `json:"state"`
	Repo         prRepoView        `json:"repo"`
	HeadSha      string            `json:"head_sha"`
	BaseRef      string            `json:"base_ref"`
	UpdatedAt    time.Time         `json:"updated_at"`
	RiskScore    *int              `json:"risk_score"`
	EffortScore  *int              `json:"effort_score"`
	ImpactScore  *int              `json:"impact_score"`
	LatestReview *latestReviewView `json:"latest_review"`
}

// intPtr proyecta el score nullable de la fila a nil/valor del JSON.
func intPtr(i int) *int { return &i }

// prViewOf proyecta la fila del listado (con corrida interpolada y conteos).
func prViewOf(row store.ListPullRequestsWithLatestReviewRow) prView {
	v := prView{
		ID:        row.ID,
		Number:    row.Number,
		Author:    row.Author,
		State:     row.State,
		Repo:      prRepoView{ID: row.RepositoryID, Owner: row.RepoOwner, Name: row.RepoName, Vcs: row.RepoVcs},
		HeadSha:   row.HeadSha,
		BaseRef:   row.BaseRef,
		UpdatedAt: row.UpdatedAt.Time,
		// Sentinel de la query: review_id = 0 ↔ PR sin corridas (los
		// COALESCE documentados en pull_requests.sql).
		LatestReview: nil,
	}
	if row.RiskScore.Valid {
		v.RiskScore = intPtr(int(row.RiskScore.Int32))
	}
	if row.EffortScore.Valid {
		v.EffortScore = intPtr(int(row.EffortScore.Int32))
	}
	if row.ImpactScore.Valid {
		v.ImpactScore = intPtr(int(row.ImpactScore.Int32))
	}
	if row.ReviewID != 0 {
		v.LatestReview = &latestReviewView{
			ID:        row.ReviewID,
			Status:    row.ReviewStatus,
			CreatedAt: row.ReviewCreatedAt.Time,
			Counts:    reviewCounts{High: row.High, Medium: row.Medium, Low: row.Low},
		}
	}
	return v
}

// queryChoice valida un query param contra un conjunto cerrado: la ausencia
// ("") siempre pasa. Fail closed (decisión 8 de F5): valor desconocido →
// 400 con mensaje claro, jamás un ignore silencioso.
func queryChoice(val string, allowed ...string) (string, bool) {
	if val == "" {
		return "", true
	}
	for _, a := range allowed {
		if val == a {
			return val, true
		}
	}
	return "", false
}

// handleListPRs: GET /api/prs — todos los PRs (los cerrados quedan: conservan
// valor de auditoría) con la última corrida. Primer query params de la API
// (decisión 8 de F5, el triage es su primer consumidor):
//   - sort=updated (default) | risk | effort | impact (F6 §6): el default
//     conserva el ORDER BY updated_at DESC del SQL; los scores reordenan
//     en el handler (mismo patrón nulls-last).
//   - state=open|closed, repo={id}, severity=high|medium|low: filtran.
//
// El sort/filter vive en Go, no en SQL dinámico (decisión del brief): un solo
// PR de sqlc, sin consulta duplicada por cada combinación.
// ponytail: sort/filter O(n) sobre el listado completo — single-org, cientos
// de PRs; mover a SQL dinámico solo si el dataset llega a millones.
func (s *Server) handleListPRs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sortBy, ok := queryChoice(q.Get("sort"), "updated", "risk", "effort", "impact")
	if !ok {
		writeError(w, http.StatusBadRequest, `valor inválido para "sort": esperado updated|risk|effort|impact`)
		return
	}
	state, ok := queryChoice(q.Get("state"), "open", "closed")
	if !ok {
		writeError(w, http.StatusBadRequest, `valor inválido para "state": esperado open|closed`)
		return
	}
	var repoID int64
	if raw := q.Get("repo"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, `valor inválido para "repo": debe ser un id numérico`)
			return
		}
		repoID = id
	}
	severity, ok := queryChoice(q.Get("severity"), "high", "medium", "low")
	if !ok {
		writeError(w, http.StatusBadRequest, `valor inválido para "severity": esperado high|medium|low`)
		return
	}

	rows, err := s.store.ListPullRequestsWithLatestReview(r.Context())
	if err != nil {
		slog.Error("listando PRs", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	views := make([]prView, 0, len(rows))
	for _, row := range rows {
		v := prViewOf(row)
		if state != "" && v.State != state {
			continue
		}
		if repoID != 0 && v.Repo.ID != repoID {
			continue
		}
		// severity: al menos un finding de esa severidad en la ÚLTIMA
		// corrida (los conteos ya vienen por corrida desde el SQL).
		if severity != "" {
			var n int64
			if v.LatestReview != nil {
				switch severity {
				case "high":
					n = v.LatestReview.Counts.High
				case "medium":
					n = v.LatestReview.Counts.Medium
				case "low":
					n = v.LatestReview.Counts.Low
				}
			}
			if n < 1 {
				continue
			}
		}
		views = append(views, v)
	}
	if sortBy != "updated" {
		// score DESC, sin score al final, empate → el orden base del SQL
		// (updated_at DESC) sobrevive gracias al sort estable. Los tres
		// scores del triage comparten el patrón (§6 F5/F6).
		switch sortBy {
		case "risk":
			sortByScoreDesc(views, func(v prView) *int { return v.RiskScore })
		case "effort":
			sortByScoreDesc(views, func(v prView) *int { return v.EffortScore })
		case "impact":
			sortByScoreDesc(views, func(v prView) *int { return v.ImpactScore })
		}
	}
	writeJSON(w, http.StatusOK, views)
}

// sortByScoreDesc reordena las vistas por el score elegido DESC con los
// nulls al final y sort estable (el orden base del SQL sobrevive en los
// empates) — el patrón único de los tres scores del triage (§6 F5/F6).
func sortByScoreDesc(views []prView, pick func(prView) *int) {
	sort.SliceStable(views, func(i, j int) bool {
		vi, vj := pick(views[i]), pick(views[j])
		switch {
		case vi == nil:
			return false
		case vj == nil:
			return true
		default:
			return *vi > *vj
		}
	})
}

// reviewView es la corrida completa para el detalle: los textos que completó
// la corrida (§3.3 nacen vacíos y se completan al finalizar).
type reviewView struct {
	ID          int64     `json:"id"`
	Status      string    `json:"status"`
	Summary     string    `json:"summary"`
	Walkthrough string    `json:"walkthrough"`
	Mermaid     string    `json:"mermaid"`
	CreatedAt   time.Time `json:"created_at"`
}

// findingView es un hallazgo del detalle. suggestion y verified son
// opcionales por diseño (§3.3): suggestion es NULL sin fix propuesto y
// verified queda null hasta que el Verifier corre (F3) — nil JSON, no false.
type findingView struct {
	ID         int64   `json:"id"`
	ReviewID   int64   `json:"review_id"`
	File       string  `json:"file"`
	Line       int32   `json:"line"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Body       string  `json:"body"`
	Suggestion *string `json:"suggestion"`
	Source     string  `json:"source"`
	Verified   *bool   `json:"verified"`
	// Accepted: el metrics job (§6 F5) marca si la sugerencia terminó
	// aplicada. Null = sin evaluar (el PR sigue abierto).
	Accepted *bool `json:"accepted"`
	// GithubCommentID: id del comentario que este hallazgo produjo en el
	// VCS. Con él el dashboard permalinkea al comentario real, donde el
	// bloque suggestion de GitHub se aplica con un clic.
	GithubCommentID *string `json:"github_comment_id"`
	// Dismissed: la huella (repo, file, category) fue marcada "no aplica"
	// por un humano (S4). El hallazgo viaja igual (auditoría §3.3); la UI
	// lo atenúa.
	Dismissed bool `json:"dismissed"`
}

// prDetailView es el cuerpo de GET /api/prs/{id}: PR + última corrida +
// todos los hallazgos de todas las corridas (atribución por corrida) + el
// change stack de la última corrida (§6 F6, T4). ChangeStack viaja como
// JSON crudo (json.RawMessage): la api no interpreta cohortes — las produce
// index — y omitempty las omite cuando no hay (null/ausente sin índice del
// repo, degradación visible).
type prDetailView struct {
	PR          prView          `json:"pr"`
	Review      *reviewView     `json:"review"`
	Findings    []findingView   `json:"findings"`
	ChangeStack json.RawMessage `json:"change_stack,omitempty"`
}

// severityRank ordena para el triage: high primero, luego file/line.
var severityRank = map[string]int{"high": 0, "medium": 1, "low": 2}

// handlePRDetail: GET /api/prs/{id} — reusa las queries existentes del
// pipeline (GetPullRequest/GetRepository/GetLatestReviewByPR/
// ListFindingsByPR): nada nuevo en el store para el detalle.
func (s *Server) handlePRDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	pr, err := s.store.GetPullRequest(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PR inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando PR", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	repo, err := s.store.GetRepository(r.Context(), pr.RepositoryID)
	if err != nil {
		// FK NOT NULL: el repo existe; ErrNoRows acá es dato corrupto y
		// responde 404 sin filtrar el stack (el detalle va al log).
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("buscando repo del PR", "err", err, "req_id", r.Context().Value(requestIDKey))
		}
		writeError(w, http.StatusNotFound, "PR inexistente")
		return
	}

	out := prDetailView{PR: prView{ID: pr.ID, Number: pr.Number, Author: pr.Author, State: pr.State,
		Repo:      prRepoView{ID: repo.ID, Owner: repo.Owner, Name: repo.Name, Vcs: repo.Vcs},
		HeadSha:   pr.HeadSha,
		BaseRef:   pr.BaseRef,
		UpdatedAt: pr.UpdatedAt.Time,
	}, Findings: []findingView{}}
	// Misma forma que el listado: el score viaja en el bloque pr (o null si
	// aún no corrió una review que lo calculara). El triage extendido
	// (§6 F6) espeja effort/impact igual que risk.
	if pr.RiskScore.Valid {
		out.PR.RiskScore = intPtr(int(pr.RiskScore.Int32))
	}
	if pr.EffortScore.Valid {
		out.PR.EffortScore = intPtr(int(pr.EffortScore.Int32))
	}
	if pr.ImpactScore.Valid {
		out.PR.ImpactScore = intPtr(int(pr.ImpactScore.Int32))
	}
	// Change stack (§6 F6, T4): []byte de la store ([] vacío = NULL/no
	// calculado) → JSON crudo o campo omitido.
	if len(pr.ChangeStack) > 0 {
		out.ChangeStack = json.RawMessage(pr.ChangeStack)
	}

	review, err := s.store.GetLatestReviewByPR(r.Context(), id)
	switch {
	case err == nil:
		out.Review = &reviewView{
			ID:          review.ID,
			Status:      review.Status,
			Summary:     review.Summary,
			Walkthrough: review.Walkthrough,
			Mermaid:     review.Mermaid,
			CreatedAt:   review.CreatedAt.Time,
		}
	case errors.Is(err, pgx.ErrNoRows):
		// PR sin corridas: review null — estado válido del listado de espera.
	default:
		slog.Error("buscando última review del PR", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	findings, err := s.store.ListFindingsByPR(r.Context(), id)
	if err != nil {
		slog.Error("listando findings del PR", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	out.Findings = findingViewsOf(findings)
	writeJSON(w, http.StatusOK, out)
}

// findingViewsOf proyecta y ordena los hallazgos: severity (high → low) y
// dentro de cada severidad file/line — el triage ataca primero lo grave.
func findingViewsOf(findings []store.ListFindingsByPRRow) []findingView {
	views := make([]findingView, 0, len(findings))
	for _, f := range findings {
		v := findingView{
			ID:       f.ID,
			ReviewID: f.ReviewID,
			File:     f.File,
			Line:     f.Line,
			Severity: f.Severity,
			Category: f.Category,
			Body:     f.Body,
			Source:   f.Source,
		}
		if f.Suggestion.Valid {
			s := f.Suggestion.String
			v.Suggestion = &s
		}
		if f.Verified.Valid {
			b := f.Verified.Bool
			v.Verified = &b
		}
		if f.Accepted.Valid {
			b := f.Accepted.Bool
			v.Accepted = &b
		}
		if f.GithubCommentID.Valid && f.GithubCommentID.String != "" {
			s := f.GithubCommentID.String
			v.GithubCommentID = &s
		}
		v.Dismissed = f.Dismissed
		views = append(views, v)
	}
	sort.SliceStable(views, func(i, j int) bool {
		ri, rj := severityRank[views[i].Severity], severityRank[views[j].Severity]
		if ri != rj {
			return ri < rj
		}
		if views[i].File != views[j].File {
			return views[i].File < views[j].File
		}
		return views[i].Line < views[j].Line
	})
	return views
}

// handlePRDiff: GET /api/prs/{id}/diff — proxy de lectura al adapter del
// repo (GetDiff, F3). 502 si el VCS falla: el error es del upstream, no del
// dashboard. Sin tope de tamaño: es dato de display (DiffMaxLines es el
// presupuesto del LLM, no del navegador).
func (s *Server) handlePRDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	pr, err := s.store.GetPullRequest(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PR inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando PR", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	repo, err := s.store.GetRepository(r.Context(), pr.RepositoryID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("buscando repo del PR", "err", err, "req_id", r.Context().Value(requestIDKey))
		}
		writeError(w, http.StatusNotFound, "PR inexistente")
		return
	}

	// El adapter lo decide repo.vcs — la api jamás sabe qué proveedor está
	// activo más allá de este dispatch (mapa: backend.vcs).
	var prov vcs.VCSProvider
	switch repo.Vcs {
	case "github":
		prov = s.github
	case "gitlab":
		prov = s.gitlab
	default:
		slog.Error("repo con vcs desconocido", "repo_id", repo.ID, "vcs", repo.Vcs,
			"req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	if prov == nil {
		// Adapter no montado en este proceso: configuración del server, no
		// culpa del request.
		slog.Error("adapter VCS no montado", "vcs", repo.Vcs, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	diff, err := prov.GetDiff(r.Context(), &repo, &pr)
	if err != nil {
		slog.Error("GetDiff del VCS", "repo_id", repo.ID, "pr_id", pr.ID, "err", err,
			"req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusBadGateway, "el VCS no entregó el diff")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"diff": diff})
}

// reviewDetailView es la respuesta de GET /api/reviews/{id}: la corrida con
// lo que no se ve en el PR — qué proveedor atendió cada rol, tokens por rol,
// duración (updated_at − created_at, el ciclo de vida de la fila) y el
// recuento de hallazgos por severidad (§9.9).
type reviewDetailView struct {
	ID         int64            `json:"id"`
	PRID       int64            `json:"pull_request_id"`
	Status     string           `json:"status"`
	CreatedAt  time.Time        `json:"created_at"`
	DurationMS int64            `json:"duration_ms"`
	Summary    string           `json:"summary"`
	LlmUsage   []llmUsageView   `json:"llm_usage"`
	Findings   map[string]int64 `json:"findings_by_severity"`
}

// llmUsageView: tokens por (rol, modelo) de la corrida.
type llmUsageView struct {
	Role      string `json:"role"`
	Model     string `json:"model"`
	TokensIn  int64  `json:"tokens_in"`
	TokensOut int64  `json:"tokens_out"`
}

// handleReviewDetail: GET /api/reviews/{id} — auditoría de una corrida.
func (s *Server) handleReviewDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	review, err := s.store.GetReview(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "corrida inexistente")
		return
	} else if err != nil {
		slog.Error("buscando corrida", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	usage, err := s.store.ListLlmUsageByReview(r.Context(), pgtype.Int8{Int64: id, Valid: true})
	if err != nil {
		slog.Error("uso LLM de la corrida", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	findings, err := s.store.ListFindingsByReview(r.Context(), id)
	if err != nil {
		slog.Error("hallazgos de la corrida", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	bySeverity := map[string]int64{"high": 0, "medium": 0, "low": 0}
	for _, f := range findings {
		bySeverity[f.Severity]++
	}
	view := reviewDetailView{
		ID: review.ID, PRID: review.PullRequestID, Status: review.Status,
		CreatedAt: review.CreatedAt.Time, Summary: review.Summary,
		DurationMS: review.UpdatedAt.Time.Sub(review.CreatedAt.Time).Milliseconds(),
		LlmUsage:   make([]llmUsageView, 0, len(usage)),
		Findings:   bySeverity,
	}
	for _, u := range usage {
		view.LlmUsage = append(view.LlmUsage, llmUsageView{
			Role: u.Role, Model: u.Model, TokensIn: u.TokensIn, TokensOut: u.TokensOut,
		})
	}
	writeJSON(w, http.StatusOK, view)
}

// handleDismissFinding: POST /api/findings/{id}/dismiss — marca la huella
// del hallazgo "no aplica" (S4): las corridas futuras del mismo repo traen
// la huella marcada y la UI la atenúa. El hallazgo no se toca (auditoría
// §3.3). Cualquier usuario autenticado: el triage es de member también (§3.4).
func (s *Server) handleDismissFinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	finding, err := s.store.GetFinding(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "hallazgo inexistente")
		return
	} else if err != nil {
		slog.Error("buscando hallazgo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	// El repo del hallazgo: el dismiss es por (repo, file, category).
	review, err := s.store.GetReview(r.Context(), finding.ReviewID)
	if err != nil {
		slog.Error("buscando la corrida del hallazgo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	pr, err := s.store.GetPullRequest(r.Context(), review.PullRequestID)
	if err != nil {
		slog.Error("buscando el PR del hallazgo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	// Cuerpo opcional: el motivo mejora la auditoría pero no es requisito.
	_ = json.NewDecoder(r.Body).Decode(&body)
	sess := sessionOf(r)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "no autenticado")
		return
	}
	if _, err := s.store.UpsertFindingDismissal(r.Context(), store.UpsertFindingDismissalParams{
		RepositoryID: pr.RepositoryID,
		File:         finding.File,
		Category:     finding.Category,
		DismissedBy:  sess.Row.UserID,
		Reason:       body.Reason,
	}); err != nil {
		slog.Error("marcando el hallazgo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": true})
}
