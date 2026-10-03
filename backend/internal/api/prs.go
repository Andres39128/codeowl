// Lectura de PRs para el dashboard (mapa: rest_endpoints — F3, guía §6
// "detalle de PR con findings y diff"). Solo lectura y visible para cualquier
// usuario autenticado (§3.4: el member opera el triage y consulta PRs) —
// auth sin CSRF, como el panel de cola. El diff baja bajo demanda vía
// GetDiff del adapter del repo (mapa: backend.api.nota — la api depende de
// vcs para GetDiff); la api jamás ejecuta análisis (§3.5).
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

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
type prView struct {
	ID           int64             `json:"id"`
	Number       int64             `json:"number"`
	Author       string            `json:"author"`
	State        string            `json:"state"`
	Repo         prRepoView        `json:"repo"`
	HeadSha      string            `json:"head_sha"`
	BaseRef      string            `json:"base_ref"`
	UpdatedAt    time.Time         `json:"updated_at"`
	LatestReview *latestReviewView `json:"latest_review"`
}

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

// handleListPRs: GET /api/prs — todos los PRs, el más reciente primero
// (updated_at DESC). Los cerrados quedan: conservan valor de auditoría.
func (s *Server) handleListPRs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListPullRequestsWithLatestReview(r.Context())
	if err != nil {
		slog.Error("listando PRs", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	views := make([]prView, 0, len(rows))
	for _, row := range rows {
		views = append(views, prViewOf(row))
	}
	writeJSON(w, http.StatusOK, views)
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
	File       string  `json:"file"`
	Line       int32   `json:"line"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Body       string  `json:"body"`
	Suggestion *string `json:"suggestion"`
	Source     string  `json:"source"`
	Verified   *bool   `json:"verified"`
}

// prDetailView es el cuerpo de GET /api/prs/{id}: PR + última corrida +
// todos los hallazgos de todas las corridas (atribución por corrida).
type prDetailView struct {
	PR       prView        `json:"pr"`
	Review   *reviewView   `json:"review"`
	Findings []findingView `json:"findings"`
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
func findingViewsOf(findings []store.Finding) []findingView {
	views := make([]findingView, 0, len(findings))
	for _, f := range findings {
		v := findingView{
			ID:       f.ID,
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
