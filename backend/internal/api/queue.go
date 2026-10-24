// Lectura del panel de cola (mapa: rest_endpoints — F1, guía §9.9):
// pending/running/discarded por tipo de job, con cantidad y última creación.
// Disponible para TODO usuario autenticado (§3.4: el member opera el triage
// y consulta el estado de la cola — revisar un job caído no requiere psql).
//
// Único SQL crudo fuera de las queries sqlc: river_job es tabla de River (su
// schema vive en las migraciones, §3.3) y no es dominio de store — leerla por
// sqlc acoplaría store al schema de River.
package api

import (
	"log/slog"
	"net/http"
	"time"
)

// jobQueueRow es una fila del panel: un (kind, state) con su cantidad y la
// creación más reciente de ese grupo (§9.9: "la última ejecución de cada uno").
type jobQueueRow struct {
	Kind            string    `json:"kind"`
	State           string    `json:"state"`
	Count           int64     `json:"count"`
	LatestCreatedAt time.Time `json:"latest_created_at"`
}

// Estados visibles en el panel. Nota: River v0.48 inserta los jobs nuevos con
// state 'available'; 'pending' quedó como estado del enum en versiones previas.
// El panel muestra ambos como "en espera" — filtrar solo por 'pending'
// dejaría el panel vacío con jobs frescos en cola.
const jobsPanelStates = `'available', 'pending', 'running', 'discarded'`

// jobHistoryRow es una fila de la historia de jobs finalizados: duración de
// ejecución (finalized_at − attempted_at) y el último error si lo hubo.
type jobHistoryRow struct {
	Kind        string    `json:"kind"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	FinalizedAt time.Time `json:"finalized_at"`
	DurationMS  int64     `json:"duration_ms"`
	Error       *string   `json:"error"`
}

// handleJobsHistory: GET /api/jobs/history — los últimos 50 jobs con estado
// final (completed/cancelled/discarded), lo más reciente primero. El panel
// de cola es el estado vivo; esto es la auditoría (§9.9: revisar un job
// caído no requiere psql).
func (s *Server) handleJobsHistory(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `
		SELECT kind, state, created_at, finalized_at,
		       (EXTRACT(EPOCH FROM (finalized_at - attempted_at)) * 1000)::bigint AS duration_ms,
		       CASE WHEN errors IS NULL OR array_length(errors, 1) IS NULL THEN NULL
		            ELSE errors[array_length(errors, 1)] END AS last_error
		FROM river_job
		WHERE state IN ('completed', 'cancelled', 'discarded') AND finalized_at IS NOT NULL
		ORDER BY finalized_at DESC
		LIMIT 50`)
	if err != nil {
		slog.Error("consultando la historia de river_job", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	defer rows.Close()

	historia := make([]jobHistoryRow, 0)
	for rows.Next() {
		var h jobHistoryRow
		var lastError *string
		if err := rows.Scan(&h.Kind, &h.State, &h.CreatedAt, &h.FinalizedAt, &h.DurationMS, &lastError); err != nil {
			slog.Error("escaneando la historia de jobs", "err", err, "req_id", r.Context().Value(requestIDKey))
			writeError(w, http.StatusInternalServerError, "error interno")
			return
		}
		// errors[] de River guarda un JSON por intento; lo legible acá es el
		// texto del último error, recortado (la fila entera va al log).
		if lastError != nil {
			msg := *lastError
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			h.Error = &msg
		}
		historia = append(historia, h)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterando la historia de jobs", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusOK, historia)
}

// handleListJobs: GET /api/jobs — agrupado por (kind, state).
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `
		SELECT kind, state, COUNT(*) AS count, MAX(created_at) AS latest
		FROM river_job
		WHERE state IN (`+jobsPanelStates+`)
		GROUP BY kind, state
		ORDER BY kind, state`)
	if err != nil {
		slog.Error("consultando river_job", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	defer rows.Close()

	jobs := make([]jobQueueRow, 0)
	for rows.Next() {
		var j jobQueueRow
		if err := rows.Scan(&j.Kind, &j.State, &j.Count, &j.LatestCreatedAt); err != nil {
			slog.Error("escaneando river_job", "err", err, "req_id", r.Context().Value(requestIDKey))
			writeError(w, http.StatusInternalServerError, "error interno")
			return
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterando river_job", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}
