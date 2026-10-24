// Trazabilidad de merges sin PR (trazabilidad-merges S1): lectura de los
// pushes/merges directos a la rama base, analizados con el pipeline completo
// (T3) y sin PR donde publicar — todo vive en el dashboard (S2). Espejo de
// los handlers de PR (prs.go): member-visible (§3.4: opera el triage y
// consulta), solo lectura — auth por sesión sin CSRF. El diff baja bajo
// demanda vía GetCommitDiff del adapter del repo (T2); la api jamás ejecuta
// análisis (§3.5).
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// pushLatestReviewView es la corrida más reciente del push, versión lista:
// sin textos (el detalle los trae completos).
type pushLatestReviewView struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// pushView es la fila del listado y el bloque "push" del detalle. Los SHAs
// viajan completos: son la identidad del merge (before...after), no un
// recorte de display. FindingsCount son los hallazgos de la ÚLTIMA corrida
// por severidad (conjunto cerrado del schema, §3.3) — espejo aplanado de
// los conteos del listado de PRs.
type pushView struct {
	ID            int64                 `json:"id"`
	Repo          prRepoView            `json:"repo"`
	Branch        string                `json:"branch"`
	BeforeSha     string                `json:"before_sha"`
	AfterSha      string                `json:"after_sha"`
	Committer     string                `json:"committer"`
	CreatedAt     time.Time             `json:"created_at"`
	LatestReview  *pushLatestReviewView `json:"latest_review"`
	FindingsCount reviewCounts          `json:"findings_count"`
}

// countsOfLatest cuenta los hallazgos de la corrida latest por severidad:
// las filas traen TODAS las corridas del push (atribución por review_id,
// §3.3), el conteo del listado mira solo la última.
func countsOfLatest(findings []store.ListFindingsByDirectPushRow, latestID int64) reviewCounts {
	counts := reviewCounts{}
	for _, f := range findings {
		if f.ReviewID != latestID {
			continue
		}
		switch f.Severity {
		case "high":
			counts.High++
		case "medium":
			counts.Medium++
		case "low":
			counts.Low++
		}
	}
	return counts
}

// handleListPushes: GET /api/pushes — todos los pushes directos (LIMIT 100
// del SQL) con su corrida más reciente y los conteos de ESA corrida. Los
// conteos bajan con una query de findings por fila (simple > clever:
// single-org, ≤100 pushes — sin query agregada extra que duplicar).
func (s *Server) handleListPushes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListDirectPushes(r.Context(), pgtype.Int8{})
	if err != nil {
		slog.Error("listando pushes", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	views := make([]pushView, 0, len(rows))
	for _, row := range rows {
		v := pushView{
			ID:        row.ID,
			Repo:      prRepoView{ID: row.RepositoryID, Owner: row.RepoOwner, Name: row.RepoName, Vcs: row.RepoVcs},
			Branch:    row.Branch,
			BeforeSha: row.BeforeSha,
			AfterSha:  row.AfterSha,
			Committer: row.Committer,
			CreatedAt: row.CreatedAt.Time,
		}
		if row.LatestReviewID.Valid {
			findings, err := s.store.ListFindingsByDirectPush(r.Context(), row.ID)
			if err != nil {
				slog.Error("contando hallazgos del push", "push", row.ID, "err", err,
					"req_id", r.Context().Value(requestIDKey))
				writeError(w, http.StatusInternalServerError, "error interno")
				return
			}
			v.LatestReview = &pushLatestReviewView{
				ID:        row.LatestReviewID.Int64,
				Status:    row.LatestReviewStatus.String,
				CreatedAt: row.LatestReviewAt.Time,
			}
			v.FindingsCount = countsOfLatest(findings, row.LatestReviewID.Int64)
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, views)
}

// pushDetailView es el cuerpo de GET /api/pushes/{id}: el push + la última
// corrida completa (textos incluidos) + TODOS los hallazgos de todas las
// corridas — la MISMA forma que el detalle de PR (T5 reusa componentes).
type pushDetailView struct {
	Push     pushView      `json:"push"`
	Review   *reviewView   `json:"review"`
	Findings []findingView `json:"findings"`
}

// toPRFindingRows convierte las filas del push a la forma de fila del PR:
// ListFindingsByDirectPush es el MISMO SELECT de ListFindingsByPR con la
// ventana del push (T4), así el detalle reusa findingViewsOf tal cual — una
// sola proyección y un solo orden (severity → file/line) para PRs y pushes.
func toPRFindingRows(rows []store.ListFindingsByDirectPushRow) []store.ListFindingsByPRRow {
	out := make([]store.ListFindingsByPRRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.ListFindingsByPRRow{
			ID:              r.ID,
			ReviewID:        r.ReviewID,
			File:            r.File,
			Line:            r.Line,
			Severity:        r.Severity,
			Category:        r.Category,
			Body:            r.Body,
			Suggestion:      r.Suggestion,
			Source:          r.Source,
			Verified:        r.Verified,
			CreatedAt:       r.CreatedAt,
			Accepted:        r.Accepted,
			GithubCommentID: r.GithubCommentID,
			Dismissed:       r.Dismissed,
		})
	}
	return out
}

// handlePushDetail: GET /api/pushes/{id} — el push + repo + última corrida
// completa (summary/walkthrough/mermaid) + hallazgos de todas sus corridas
// con la misma forma y orden que el detalle de PR. Reusa las queries nuevas
// del store (GetLatestReviewByDirectPush/ListFindingsByDirectPush); sin
// corridas → review null (estado válido: aún en cola).
func (s *Server) handlePushDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	push, err := s.store.GetDirectPush(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "push inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando push", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	repo, err := s.store.GetRepository(r.Context(), push.RepositoryID)
	if err != nil {
		// FK NOT NULL: el repo existe; ErrNoRows acá es dato corrupto (espejo
		// del detalle de PR) — 404 sin filtrar el stack.
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("buscando repo del push", "err", err, "req_id", r.Context().Value(requestIDKey))
		}
		writeError(w, http.StatusNotFound, "push inexistente")
		return
	}

	out := pushDetailView{
		Push: pushView{
			ID:        push.ID,
			Repo:      prRepoView{ID: repo.ID, Owner: repo.Owner, Name: repo.Name, Vcs: repo.Vcs},
			Branch:    push.Branch,
			BeforeSha: push.BeforeSha,
			AfterSha:  push.AfterSha,
			Committer: push.Committer,
			CreatedAt: push.CreatedAt.Time,
		},
		Findings: []findingView{},
	}
	review, err := s.store.GetLatestReviewByDirectPush(r.Context(), id)
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
		out.Push.LatestReview = &pushLatestReviewView{
			ID:        review.ID,
			Status:    review.Status,
			CreatedAt: review.CreatedAt.Time,
		}
	case errors.Is(err, pgx.ErrNoRows):
		// Push sin corridas: review null — estado válido del listado.
	default:
		slog.Error("buscando última review del push", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	rows, err := s.store.ListFindingsByDirectPush(r.Context(), id)
	if err != nil {
		slog.Error("listando findings del push", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	out.Findings = findingViewsOf(toPRFindingRows(rows))
	// Conteos de la última corrida (misma forma que el listado): gratis, los
	// hallazgos ya están proyectados.
	if out.Review != nil {
		latest := *out.Push.LatestReview
		counts := reviewCounts{}
		for _, f := range out.Findings {
			if f.ReviewID != latest.ID {
				continue
			}
			switch f.Severity {
			case "high":
				counts.High++
			case "medium":
				counts.Medium++
			case "low":
				counts.Low++
			}
		}
		out.Push.FindingsCount = counts
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePushDiff: GET /api/pushes/{id}/diff — proxy de lectura al adapter
// del repo (GetCommitDiff, T2): el diff before...after del merge directo.
// Espejo de handlePRDiff: 502 si el VCS falla (el error es del upstream, no
// del dashboard); sin tope de tamaño (dato de display, §3.6).
func (s *Server) handlePushDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	push, err := s.store.GetDirectPush(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "push inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando push", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	repo, err := s.store.GetRepository(r.Context(), push.RepositoryID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("buscando repo del push", "err", err, "req_id", r.Context().Value(requestIDKey))
		}
		writeError(w, http.StatusNotFound, "push inexistente")
		return
	}

	// El adapter lo decide repo.vcs — mismo dispatch que handlePRDiff.
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

	diff, err := prov.GetCommitDiff(r.Context(), &repo, push.BeforeSha, push.AfterSha)
	if err != nil {
		slog.Error("GetCommitDiff del VCS", "repo_id", repo.ID, "push_id", push.ID, "err", err,
			"req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusBadGateway, "el VCS no entregó el diff")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"diff": diff})
}
