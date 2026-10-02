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
