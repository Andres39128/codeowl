package api

// Tests de GET /api/metrics (T7, decisión 8): tasas y nulls de la vista,
// auth member-visible y matemática exacta contra el Postgres de desarrollo.
// La BD es compartida: la prueba de la ventana captura un baseline ANTES de
// sembrar y compara el delta (lo que deja el test f1/f5 como precedente —
// PRs merged ajenos quedan en la BD y entrarían en la ventana).

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// composeMetrics es pura: los nulls (denominador 0) y las tasas se prueban
// sin BD de por medio.
func TestComposeMetricsTasasYNulos(t *testing.T) {
	vacio := composeMetrics(30, store.GetMetricsSummaryRow{}, store.GetAvgLlmTokensPerReviewRow{})
	if vacio.MergedPrs != 0 || vacio.FindingsWithOutcome != 0 || vacio.Accepted != 0 ||
		vacio.ResolvedComments != 0 || vacio.FalsePositives != 0 || vacio.ReviewsWithCost != 0 {
		t.Errorf("ventana sin datos debe contar 0: %+v", vacio)
	}
	if vacio.AvgCycleTimeHours != nil || vacio.AcceptedRate != nil ||
		vacio.FalsePositiveRate != nil || vacio.AvgTokensPerReview != nil {
		t.Errorf("denominador 0 debe ser null, nunca 0%%: %+v", vacio)
	}
	if vacio.WindowDays != 30 {
		t.Errorf("window_days debe ecoar el pedido: %+v", vacio)
	}

	lleno := composeMetrics(7,
		store.GetMetricsSummaryRow{
			MergedPrs: 2, AvgCycleTimeHours: 30.5,
			EvaluatedFindings: 4, AcceptedFindings: 1,
			ResolvedComments: 3, FalsePositives: 1,
		},
		store.GetAvgLlmTokensPerReviewRow{AvgTokensPerReview: 27.5, ReviewsCounted: 2})
	if lleno.AvgCycleTimeHours == nil || *lleno.AvgCycleTimeHours != 30.5 {
		t.Errorf("cycle time debe salir del resumen: %+v", lleno.AvgCycleTimeHours)
	}
	if lleno.AcceptedRate == nil || math.Abs(*lleno.AcceptedRate-0.25) > 0.0001 {
		t.Errorf("accepted_rate debe ser 1/4: %+v", lleno.AcceptedRate)
	}
	// FP = resolved ∧ ¬applied sobre resueltos: 1 de 3.
	if lleno.FalsePositiveRate == nil || math.Abs(*lleno.FalsePositiveRate-1.0/3.0) > 0.0001 {
		t.Errorf("false_positive_rate debe ser 1/3: %+v", lleno.FalsePositiveRate)
	}
	if lleno.AvgTokensPerReview == nil || *lleno.AvgTokensPerReview != 27.5 {
		t.Errorf("avg_tokens debe salir del costo por review: %+v", lleno.AvgTokensPerReview)
	}
}

// Member-visible como los PRs (§3.4): sin sesión 401, member 200.
func TestMetricsAuthYMemberVisible(t *testing.T) {
	e := newTestEnv(t, 5)
	m := e.member(t)

	if got := e.do(t, http.MethodGet, "/api/metrics", nil, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("GET /api/metrics sin sesión debe ser 401, fue %d", got)
	}
	if got := e.do(t, http.MethodGet, "/api/metrics", m.cookie, m.csrf, nil).StatusCode; got != http.StatusOK {
		t.Errorf("GET /api/metrics como member debe ser 200, fue %d", got)
	}
}

// days: no numérico → 400 (fail closed); fuera de banda → clamp 1..365;
// válido → ecoa window_days.
func TestMetricsDaysClampYInvalido(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)

	casos := []struct {
		query   string
		status  int
		wantLen int
	}{
		{"?days=abc", http.StatusBadRequest, 0},
		{"?days=0", http.StatusOK, 1},
		{"?days=9999", http.StatusOK, 365},
		{"?days=7", http.StatusOK, 7},
	}
	for _, c := range casos {
		resp := e.do(t, http.MethodGet, "/api/metrics"+c.query, a.cookie, a.csrf, nil)
		if resp.StatusCode != c.status {
			t.Errorf("%s debe ser %d, fue %d", c.query, c.status, resp.StatusCode)
			continue
		}
		if c.status != http.StatusOK {
			continue
		}
		var out metricsView
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.WindowDays != c.wantLen {
			t.Errorf("%s debe ecoar window_days=%d, fue %d", c.query, c.wantLen, out.WindowDays)
		}
	}
}

// Matemática exacta sobre datos sembrados (espejo del TestF5MetricsSummary,
// pero vía HTTP): 2 PRs merged con cycle 48h2s y 12h2s, 1 aceptado de 2
// evaluados, 1 FP de 2 resueltos, costo 45+10 en 2 reviews (promedio 27.5).
// El delta contra el baseline absorbe lo que quede de otras corridas en la
// BD compartida.
func TestMetricsVentanaSembrada(t *testing.T) {
	e := newTestEnv(t, 5)
	ctx := context.Background()
	desde := func() pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -365), Valid: true}
	}
	base, err := e.st.GetMetricsSummary(ctx, desde())
	if err != nil {
		t.Fatalf("baseline summary: %v", err)
	}
	tokBase, err := e.st.GetAvgLlmTokensPerReview(ctx, desde())
	if err != nil {
		t.Fatalf("baseline tokens: %v", err)
	}

	// Siembra (timestamps explícitos: cycle time exacto, reloj de BD afuera).
	inicio := time.Now()
	mergedAt := pgtype.Timestamptz{Time: inicio.Add(2 * time.Second), Valid: true}
	repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "github", ExternalID: inicio.UnixNano()%1_000_000 + 1, Owner: "acme", Name: "metrics-api-test",
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	sembrar := func(number int64, state string, createdAt time.Time, merged bool) store.PullRequest {
		t.Helper()
		p, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
			RepositoryID: repo.ID, Number: number, Author: "autora", State: state,
			HeadSha: "h-metrics", BaseRef: "main", BaseSha: "b",
			MergedAt:  pgtype.Timestamptz{Time: mergedAt.Time, Valid: merged},
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		})
		if err != nil {
			t.Fatalf("UpsertPullRequest: %v", err)
		}
		return p
	}
	// Repo fresco por corrida: números 1 y 2 sin colisión (el upsert es
	// por (repository_id, number) — repetir el número pisa la fila).
	prSlow := sembrar(1, "closed", inicio.Add(-48*time.Hour), true)
	prFast := sembrar(2, "closed", inicio.Add(-12*time.Hour), true)

	revSlow, err := e.st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: prSlow.ID, HeadSha: "hs", BaseSha: "bs"})
	if err != nil {
		t.Fatal(err)
	}
	revFast, err := e.st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: prFast.ID, HeadSha: "hf", BaseSha: "bf"})
	if err != nil {
		t.Fatal(err)
	}

	// Findings: 1 aceptado + 1 rechazado (evaluados 2). El tercero queda
	// sin outcome y no entra en el denominador.
	fAcc, err := e.st.CreateFinding(ctx, store.CreateFindingParams{
		ReviewID: revSlow.ID, File: "main.go", Line: 1, Severity: "high",
		Category: "security", Body: "acc", Source: "llm",
	})
	if err != nil {
		t.Fatal(err)
	}
	fRej, err := e.st.CreateFinding(ctx, store.CreateFindingParams{
		ReviewID: revSlow.ID, File: "main.go", Line: 2, Severity: "low",
		Category: "style", Body: "rej", Source: "sast",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateFindingAccepted(ctx, store.UpdateFindingAcceptedParams{ID: fAcc.ID, Accepted: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateFindingAccepted(ctx, store.UpdateFindingAcceptedParams{ID: fRej.ID, Accepted: pgtype.Bool{Bool: false, Valid: true}}); err != nil {
		t.Fatal(err)
	}

	// Inline: resolved∧applied (ok) y resolved∧¬applied (FP).
	csOK, err := e.st.CreateCommentSent(ctx, store.CreateCommentSentParams{
		PullRequestID: prSlow.ID, ReviewID: pgtype.Int8{Int64: revSlow.ID, Valid: true},
		CommentID: "gh-m-ok", Type: "inline",
		File: pgtype.Text{String: "main.go", Valid: true}, Category: pgtype.Text{String: "security", Valid: true},
		Anchor: pgtype.Text{String: "gh-m-ok", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	csFP, err := e.st.CreateCommentSent(ctx, store.CreateCommentSentParams{
		PullRequestID: prSlow.ID, ReviewID: pgtype.Int8{Int64: revSlow.ID, Valid: true},
		CommentID: "gh-m-fp", Type: "inline",
		File: pgtype.Text{String: "main.go", Valid: true}, Category: pgtype.Text{String: "security", Valid: true},
		Anchor: pgtype.Text{String: "gh-m-fp", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateCommentsSentResolved(ctx, store.UpdateCommentsSentResolvedParams{ID: csOK.ID, Resolved: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateCommentsSentApplied(ctx, store.UpdateCommentsSentAppliedParams{ID: csOK.ID, Applied: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateCommentsSentResolved(ctx, store.UpdateCommentsSentResolvedParams{ID: csFP.ID, Resolved: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateCommentsSentApplied(ctx, store.UpdateCommentsSentAppliedParams{ID: csFP.ID, Applied: pgtype.Bool{Bool: false, Valid: true}}); err != nil {
		t.Fatal(err)
	}

	// Costo: 45 tokens en revSlow (2 llamadas) y 10 en revFast (1 llamada).
	uso := func(revID int64, in, out int32) {
		t.Helper()
		if _, err := e.st.CreateLlmUsage(ctx, store.CreateLlmUsageParams{
			ReviewID: pgtype.Int8{Int64: revID, Valid: true},
			Role:     "review", Provider: "stub", Model: "stub-model",
			TokensIn: in, TokensOut: out,
		}); err != nil {
			t.Fatalf("CreateLlmUsage: %v", err)
		}
	}
	uso(revSlow.ID, 10, 5)
	uso(revSlow.ID, 20, 10)
	uso(revFast.ID, 7, 3)

	t.Cleanup(func() {
		revIDs := []int64{revSlow.ID, revFast.ID}
		prIDs := []int64{prSlow.ID, prFast.ID}
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM llm_usage WHERE review_id = ANY($1)", revIDs); err != nil {
			t.Errorf("limpiando llm_usage: %v", err)
		}
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM findings WHERE review_id = ANY($1)", revIDs); err != nil {
			t.Errorf("limpiando findings: %v", err)
		}
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM comments_sent WHERE pull_request_id = ANY($1)", prIDs); err != nil {
			t.Errorf("limpiando comments_sent: %v", err)
		}
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM reviews WHERE pull_request_id = ANY($1)", prIDs); err != nil {
			t.Errorf("limpiando reviews: %v", err)
		}
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM pull_requests WHERE repository_id = $1", repo.ID); err != nil {
			t.Errorf("limpiando PRs: %v", err)
		}
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repo.ID); err != nil {
			t.Errorf("limpiando repos: %v", err)
		}
	})

	// Esperado = baseline + nuestro delta, pasado por la misma proyección
	// (pura: ya probada arriba con números exactos).
	ciclo := (mergedAt.Time.Sub(inicio.Add(-48*time.Hour)).Hours() +
		mergedAt.Time.Sub(inicio.Add(-12*time.Hour)).Hours())
	esperadoSum := store.GetMetricsSummaryRow{
		MergedPrs:         base.MergedPrs + 2,
		AvgCycleTimeHours: (base.AvgCycleTimeHours*float64(base.MergedPrs) + ciclo) / float64(base.MergedPrs+2),
		EvaluatedFindings: base.EvaluatedFindings + 2,
		AcceptedFindings:  base.AcceptedFindings + 1,
		ResolvedComments:  base.ResolvedComments + 2,
		FalsePositives:    base.FalsePositives + 1,
	}
	esperadoTok := store.GetAvgLlmTokensPerReviewRow{
		ReviewsCounted:     tokBase.ReviewsCounted + 2,
		AvgTokensPerReview: (tokBase.AvgTokensPerReview*float64(tokBase.ReviewsCounted) + 55) / float64(tokBase.ReviewsCounted+2),
	}
	queremos := composeMetrics(365, esperadoSum, esperadoTok)

	a := e.admin(t)
	resp := e.do(t, http.MethodGet, "/api/metrics?days=365", a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/metrics debe ser 200, fue %d", resp.StatusCode)
	}
	var got metricsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got.MergedPrs != queremos.MergedPrs {
		t.Errorf("merged_prs = %d, querés %d", got.MergedPrs, queremos.MergedPrs)
	}
	if got.FindingsWithOutcome != queremos.FindingsWithOutcome || got.Accepted != queremos.Accepted {
		t.Errorf("aceptados = %d/%d, querés %d/%d", got.Accepted, got.FindingsWithOutcome, queremos.Accepted, queremos.FindingsWithOutcome)
	}
	if got.ResolvedComments != queremos.ResolvedComments || got.FalsePositives != queremos.FalsePositives {
		t.Errorf("FP = %d/%d, querés %d/%d", got.FalsePositives, got.ResolvedComments, queremos.FalsePositives, queremos.ResolvedComments)
	}
	if got.ReviewsWithCost != queremos.ReviewsWithCost {
		t.Errorf("reviews_with_cost = %d, querés %d", got.ReviewsWithCost, queremos.ReviewsWithCost)
	}
	if got.AvgCycleTimeHours == nil || queremos.AvgCycleTimeHours == nil ||
		math.Abs(*got.AvgCycleTimeHours-*queremos.AvgCycleTimeHours) > 0.001 {
		t.Errorf("avg_cycle_time_hours = %s, querés %s", fstr(got.AvgCycleTimeHours), fstr(queremos.AvgCycleTimeHours))
	}
	if got.AcceptedRate == nil || queremos.AcceptedRate == nil ||
		math.Abs(*got.AcceptedRate-*queremos.AcceptedRate) > 0.0001 {
		t.Errorf("accepted_rate = %s, querés %s", fstr(got.AcceptedRate), fstr(queremos.AcceptedRate))
	}
	if got.FalsePositiveRate == nil || queremos.FalsePositiveRate == nil ||
		math.Abs(*got.FalsePositiveRate-*queremos.FalsePositiveRate) > 0.0001 {
		t.Errorf("false_positive_rate = %s, querés %s", fstr(got.FalsePositiveRate), fstr(queremos.FalsePositiveRate))
	}
	if got.AvgTokensPerReview == nil || queremos.AvgTokensPerReview == nil ||
		math.Abs(*got.AvgTokensPerReview-*queremos.AvgTokensPerReview) > 0.001 {
		t.Errorf("avg_tokens_per_review = %s, querés %s", fstr(got.AvgTokensPerReview), fstr(queremos.AvgTokensPerReview))
	}
	if got.WindowDays != 365 {
		t.Errorf("window_days = %d, querés 365", got.WindowDays)
	}
}

// fstr imprime el float nullable para los mensajes de error.
func fstr(p *float64) string {
	if p == nil {
		return "null"
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}
