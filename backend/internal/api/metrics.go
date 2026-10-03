// Métricas F5 (guía §6 "métricas", decisión 8): lectura member-visible para
// el dashboard — cycle time, % aceptados, tasa FP y costo LLM por review.
// Solo lectura, auth sin CSRF (igual que /api/prs y /api/jobs): la api jamás
// ejecuta análisis (§3.5), acá solo se agregan filas ya escritas por el
// MetricsJob (T5) y el gateway.
package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// metricsView es la respuesta de GET /api/metrics. Diseño (decisión 8 de F5):
//   - Las tasas viajan como float 0..1 o null cuando el denominador es 0 —
//     null honesto, no 0% disfrazado de dato.
//   - avg_cycle_time_hours es null si no hubo PRs merged en la ventana
//     (sentinel MergedPrs==0 de GetMetricsSummary — sqlc no infiere
//     nullabilidad del COALESCE).
//   - Costo = tokens (in+out), sin pricing por modelo modelado (§6 F5): el
//     promedio es sobre totales por review, no por llamada.
type metricsView struct {
	WindowDays          int      `json:"window_days"`
	MergedPrs           int64    `json:"merged_prs"`
	AvgCycleTimeHours   *float64 `json:"avg_cycle_time_hours"`
	FindingsWithOutcome int64    `json:"findings_with_outcome"`
	Accepted            int64    `json:"accepted"`
	AcceptedRate        *float64 `json:"accepted_rate"`
	ResolvedComments    int64    `json:"resolved_comments"`
	FalsePositives      int64    `json:"false_positives"`
	FalsePositiveRate   *float64 `json:"false_positive_rate"`
	ReviewsWithCost     int64    `json:"reviews_with_cost"`
	AvgTokensPerReview  *float64 `json:"avg_tokens_per_review"`
}

// metricsDaysDefault es la ventana por defecto (guía §6: "merged últimos
// 30d"); metricsDaysMax es el tope duro del parámetro days.
const (
	metricsDaysDefault = 30
	metricsDaysMax     = 365
)

// composeMetrics proyecta los conteos crudos del store a la vista: aquí vive
// la matemática de tasas (numerador/denominador, null si denominador 0).
// Función pura para poder probar los nulls sin BD compartida de por medio.
func composeMetrics(windowDays int, sum store.GetMetricsSummaryRow, tok store.GetAvgLlmTokensPerReviewRow) metricsView {
	out := metricsView{
		WindowDays:          windowDays,
		MergedPrs:           sum.MergedPrs,
		AvgCycleTimeHours:   nil,
		FindingsWithOutcome: sum.EvaluatedFindings,
		Accepted:            sum.AcceptedFindings,
		AcceptedRate:        nil,
		ResolvedComments:    sum.ResolvedComments,
		FalsePositives:      sum.FalsePositives,
		FalsePositiveRate:   nil,
		ReviewsWithCost:     tok.ReviewsCounted,
		AvgTokensPerReview:  nil,
	}
	if sum.MergedPrs > 0 {
		out.AvgCycleTimeHours = &sum.AvgCycleTimeHours
	}
	if sum.EvaluatedFindings > 0 {
		rate := float64(sum.AcceptedFindings) / float64(sum.EvaluatedFindings)
		out.AcceptedRate = &rate
	}
	if sum.ResolvedComments > 0 {
		// FP = resolved ∧ ¬applied (§6 F5); denominador = resueltos.
		rate := float64(sum.FalsePositives) / float64(sum.ResolvedComments)
		out.FalsePositiveRate = &rate
	}
	if tok.ReviewsCounted > 0 {
		out.AvgTokensPerReview = &tok.AvgTokensPerReview
	}
	return out
}

// handleMetrics: GET /api/metrics — resumen de la ventana (30d por defecto,
// ?days=N clampeado a 1..365; no numérico → 400, fail closed como el resto
// de los query params de la API). Dos queries, cero agregación en memoria:
// los numeradores ya los calculó el SQL de T1.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	days := metricsDaysDefault
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, `valor inválido para "days": debe ser un entero`)
			return
		}
		// Clamp a la banda útil: el dashboard nunca pide una ventana de 0
		// días ni de una década.
		switch {
		case n < 1:
			n = 1
		case n > metricsDaysMax:
			n = metricsDaysMax
		}
		days = n
	}
	since := pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -days), Valid: true}

	sum, err := s.store.GetMetricsSummary(r.Context(), since)
	if err != nil {
		slog.Error("calculando resumen de métricas", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	tok, err := s.store.GetAvgLlmTokensPerReview(r.Context(), since)
	if err != nil {
		slog.Error("calculando costo LLM por review", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusOK, composeMetrics(days, sum, tok))
}
