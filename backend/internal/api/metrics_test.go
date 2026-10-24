package api

// Tests de GET /api/metrics (T7, decisión 8): tasas y nulls de la vista,
// auth member-visible y matemática exacta contra el Postgres de desarrollo.
// La BD es compartida: la prueba de la ventana captura un baseline ANTES de
// sembrar y compara el delta (lo que deja el test f1/f5 como precedente —
// PRs merged ajenos quedan en la BD y entrarían en la ventana).

import (
	"context"
	"encoding/json"
	"fmt"
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
	t.Cleanup(func() {
		if err := e.st.DeleteRepositoryCascade(ctx, repo.ID); err != nil {
			t.Errorf("borrando el repo de test: %v", err)
		}
	})
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

// TestMetricsSerieSemanal (S3): la serie cuenta PRs (distinto), no corridas,
// y solo incluye semanas con actividad dentro de la ventana.
func TestMetricsSerieSemanal(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)
	ctx := context.Background()

	now := time.Now().UnixNano()
	repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "github", ExternalID: now, Owner: "test", Name: "serie",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Dos PRs: el mismo PR con dos corridas (cuenta 1 PR) y otro con una.
	pr1, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 1, Author: "ana", State: "open",
		HeadSha: "h1", BaseRef: "main", BaseSha: "b1",
		CreatedAt: pgtype.Timestamptz{Time: time.Now().Add(-48 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	pr2, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 2, Author: "ana", State: "open",
		HeadSha: "h2", BaseRef: "main", BaseSha: "b2",
		CreatedAt: pgtype.Timestamptz{Time: time.Now().Add(-48 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// La semana sembrada se aísla pisando created_at: la base de desarrollo
	// tiene corridas reales en la semana actual y la serie es global. La
	// semana se pide al mismo SQL que produce la serie (date_trunc week) —
	// calcularla en Go duplicaría la definición de "semana".
	// Semana futura (+35d): ninguna corrida real la puebla, y la ventana de
	// 60 días la alcanza desde el presente.
	sembrada := time.Now().AddDate(0, 0, 35)
	var weekSembrada string
	if err := e.st.Pool.QueryRow(ctx,
		"SELECT to_char(date_trunc('week', $1::timestamptz), 'YYYY-MM-DD')", sembrada).Scan(&weekSembrada); err != nil {
		t.Fatal(err)
	}
	creadas := []int64{}
	for _, pr := range []store.PullRequest{pr1, pr1, pr2} {
		rev, err := e.st.CreateReview(ctx, store.CreateReviewParams{
			PullRequestID: pr.ID, HeadSha: pr.HeadSha, BaseSha: pr.BaseSha,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.Pool.Exec(ctx,
			"UPDATE reviews SET created_at=$1 WHERE id=$2", sembrada, rev.ID); err != nil {
			t.Fatal(err)
		}
		creadas = append(creadas, rev.ID)
	}
	t.Cleanup(func() {
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM reviews WHERE id = ANY($1)", creadas)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM pull_requests WHERE repository_id = $1", repo.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repo.ID)
	})

	resp := e.do(t, http.MethodGet, "/api/metrics?days=60", a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var view metricsView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	var sembradaPoint *weeklySeriesPoint
	for i := range view.ReviewsPerWeek {
		if view.ReviewsPerWeek[i].Week == weekSembrada {
			sembradaPoint = &view.ReviewsPerWeek[i]
			break
		}
	}
	if sembradaPoint == nil {
		t.Fatalf("la semana sembrada %s no está en la serie: %+v", weekSembrada, view.ReviewsPerWeek)
	}
	if sembradaPoint.Prs != 2 {
		t.Errorf("prs = %d, querés 2 (pr1 con 2 corridas cuenta 1)", sembradaPoint.Prs)
	}
	if sembradaPoint.Reviews != 3 {
		t.Errorf("reviews = %d, querés 3 corridas", sembradaPoint.Reviews)
	}
}

// Tasa de resolución por repo/semana (resolucion-hallazgos S3): hallazgos
// de la corrida anterior que desaparecieron (resueltos) vs los que
// reaparecieron (persistentes). Huellas dismissadas no cuentan (S4).
func TestMetricsResolucionPorSemana(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)
	ctx := context.Background()

	now := time.Now().UnixNano()
	repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "github", ExternalID: now, Owner: "acme", Name: "resolucion-metrics-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.st.DeleteRepositoryCascade(ctx, repo.ID); err != nil {
			t.Errorf("borrando el repo de test: %v", err)
		}
	})
	pr, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 7, Author: "ana", State: "open",
		HeadSha: "h1", BaseRef: "main", BaseSha: "b1",
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	r1, err := e.st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h1", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h2", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	crear := func(reviewID int64, file string, line int32, sev, cat string) {
		t.Helper()
		if _, err := e.st.CreateFinding(ctx, store.CreateFindingParams{
			ReviewID: reviewID, File: file, Line: line, Severity: sev,
			Category: cat, Body: "cuerpo", Source: "sast",
		}); err != nil {
			t.Fatalf("CreateFinding(%s): %v", file, err)
		}
	}
	// Corrida anterior: uno persistirá, uno se resolverá, uno se dismissa.
	crear(r1.ID, "pers.go", 1, "low", "style")
	crear(r1.ID, "reso.go", 2, "medium", "style")
	fdisc, err := e.st.CreateFinding(ctx, store.CreateFindingParams{
		ReviewID: r1.ID, File: "disc.go", Line: 3, Severity: "high", Category: "security", Body: "no aplica", Source: "sast",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Última corrida: solo reaparece pers.go.
	crear(r2.ID, "pers.go", 9, "low", "style")

	// Dismiss por la interfaz pública.
	if resp := e.do(t, http.MethodPost, fmt.Sprintf("/api/findings/%d/dismiss", fdisc.ID), a.cookie, a.csrf,
		map[string]string{"reason": "falso positivo"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss = %d, querés 200", resp.StatusCode)
	}

	resp := e.do(t, http.MethodGet, "/api/metrics?days=365", a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/metrics = %d, querés 200", resp.StatusCode)
	}
	var view metricsView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}

	// Solo la fila del repo sembrado: la BD de desarrollo puede tener más.
	var fila *resolutionPoint
	for i := range view.ResolutionPerWeek {
		if view.ResolutionPerWeek[i].Repository == "acme/resolucion-metrics-test" {
			fila = &view.ResolutionPerWeek[i]
			break
		}
	}
	if fila == nil {
		t.Fatalf("falta la fila del repo sembrado: %+v", view.ResolutionPerWeek)
	}
	// pers.go persiste, reso.go se resuelve; disc.go dismissado no cuenta
	// (si contara, resolved sería 2).
	if fila.Resolved != 1 || fila.Persistent != 1 {
		t.Errorf("resolved=%d persistent=%d, querés 1 y 1 (disc.go excluido)", fila.Resolved, fila.Persistent)
	}
	if fila.ResolutionRate != 0.5 {
		t.Errorf("resolution_rate = %v, querés 0.5", fila.ResolutionRate)
	}
	// Semana ISO (lunes) de la corrida NUEVA: r2 cayó ahora.
	n := time.Now().UTC()
	lunes := n.AddDate(0, 0, -((int(n.Weekday())+6)%7)).Format("2006-01-02")
	if fila.Week != lunes {
		t.Errorf("week = %s, querés %s (lunes de esta semana)", fila.Week, lunes)
	}
}
