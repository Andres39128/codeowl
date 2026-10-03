package store

// Tests F5 de métricas (guía §6 F5): round-trips de los outcomes (risk_score,
// resolved/applied/accepted) y matemática del resumen de métricas contra el
// Postgres de desarrollo vía testStore (store_test.go). La BD es compartida:
// la ventana del resumen arranca en el instante de inicio del test (los tests
// corren en serie, así que todo lo anterior a `since` queda afuera) y los
// timestamps de los PRs sembrados son explícitos, lo que hace el cycle time
// exacto e independiente del reloj de la BD.

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// f5Finding siembra un finding de prueba colgado de rev.
func f5Finding(t *testing.T, st *Store, revID int64, body string) Finding {
	t.Helper()
	f, err := st.CreateFinding(context.Background(), CreateFindingParams{
		ReviewID: revID, File: "main.go", Line: 1,
		Severity: "high", Category: "security", Body: body, Source: "llm",
		Verified: pgtype.Bool{},
	})
	if err != nil {
		t.Fatalf("CreateFinding(%s): %v", body, err)
	}
	return f
}

// f5Inline siembra un comentario inline con huella, colgado de rev.
func f5Inline(t *testing.T, st *Store, prID, revID int64, commentID string) CommentsSent {
	t.Helper()
	cs, err := st.CreateCommentSent(context.Background(), CreateCommentSentParams{
		PullRequestID: prID, ReviewID: pgtype.Int8{Int64: revID, Valid: true},
		CommentID: commentID, Type: "inline",
		File: f1Text("main.go"), Category: f1Text("security"), Anchor: f1Text(commentID),
	})
	if err != nil {
		t.Fatalf("CreateCommentSent(%s): %v", commentID, err)
	}
	return cs
}

func TestF5OutcomeRoundTrips(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := f1Repo(t, st)
	pr := f1PR(t, st, repo.ID, 1)
	rev, err := st.CreateReview(ctx, CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h1", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}

	// risk_score: null al nacer; el tail de Run (T3) lo pisa en cada corrida.
	if got, err := st.GetPullRequest(ctx, pr.ID); err != nil || got.RiskScore.Valid {
		t.Fatalf("risk_score debe nacer null: got %+v err=%v", got.RiskScore, err)
	}
	if err := st.UpdatePullRequestRiskScore(ctx, UpdatePullRequestRiskScoreParams{ID: pr.ID, RiskScore: pgtype.Int4{Int32: 87, Valid: true}}); err != nil {
		t.Fatalf("UpdatePullRequestRiskScore: %v", err)
	}
	if got, err := st.GetPullRequest(ctx, pr.ID); err != nil || !got.RiskScore.Valid || got.RiskScore.Int32 != 87 {
		t.Errorf("risk_score round-trip: got %+v err=%v", got.RiskScore, err)
	}

	// resolved/applied: null al nacer (thread sin evaluar); el MetricsJob
	// (T5) escribe ambos al cierre del PR.
	cs := f5Inline(t, st, pr.ID, rev.ID, "gh-f5-1")
	if cs.Resolved.Valid || cs.Applied.Valid {
		t.Errorf("resolved/applied deben nacer null: got %+v", cs)
	}
	if err := st.UpdateCommentsSentResolved(ctx, UpdateCommentsSentResolvedParams{ID: cs.ID, Resolved: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatalf("UpdateCommentsSentResolved: %v", err)
	}
	if err := st.UpdateCommentsSentApplied(ctx, UpdateCommentsSentAppliedParams{ID: cs.ID, Applied: pgtype.Bool{Bool: false, Valid: true}}); err != nil {
		t.Fatalf("UpdateCommentsSentApplied: %v", err)
	}
	inlines, err := st.GetCommentsSentByPRAndType(ctx, GetCommentsSentByPRAndTypeParams{PullRequestID: pr.ID, Type: "inline"})
	if err != nil || len(inlines) != 1 {
		t.Fatalf("GetCommentsSentByPRAndType: n=%d err=%v", len(inlines), err)
	}
	if !inlines[0].Resolved.Valid || !inlines[0].Resolved.Bool || !inlines[0].Applied.Valid || inlines[0].Applied.Bool {
		t.Errorf("round-trip de resolved/applied: got %+v", inlines[0])
	}

	// accepted: null al nacer; espejo por finding de la heurística (T5).
	f := f5Finding(t, st, rev.ID, "f5-accepted")
	if f.Accepted.Valid {
		t.Errorf("accepted debe nacer null: got %+v", f)
	}
	if err := st.UpdateFindingAccepted(ctx, UpdateFindingAcceptedParams{ID: f.ID, Accepted: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatalf("UpdateFindingAccepted: %v", err)
	}
	lista, err := st.ListFindingsByReview(ctx, rev.ID)
	if err != nil || len(lista) != 1 {
		t.Fatalf("ListFindingsByReview: n=%d err=%v", len(lista), err)
	}
	if !lista[0].Accepted.Valid || !lista[0].Accepted.Bool {
		t.Errorf("accepted round-trip: got %+v", lista[0].Accepted)
	}
}

func TestF5MetricsSummary(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	since := time.Now() // ventana del test: todo lo anterior de la BD compartida queda afuera
	repo := f1Repo(t, st)

	// PRs con timestamps explícitos: cycle time exacto (48h2s y 12h2s).
	mergedAt := since.Add(2 * time.Second)
	sembrar := func(number int64, state string, createdAt, mergedAt time.Time, merged bool) PullRequest {
		t.Helper()
		p, err := st.UpsertPullRequest(ctx, UpsertPullRequestParams{
			RepositoryID: repo.ID, Number: number, Author: "autora", State: state,
			HeadSha: fmt.Sprintf("h-%d", number), BaseRef: "main", BaseSha: "b",
			MergedAt:  pgtype.Timestamptz{Time: mergedAt, Valid: merged},
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		})
		if err != nil {
			t.Fatalf("UpsertPullRequest(%d): %v", number, err)
		}
		return p
	}
	prSlow := sembrar(1, "closed", since.Add(-48*time.Hour), mergedAt, true)
	prFast := sembrar(2, "closed", since.Add(-12*time.Hour), mergedAt, true)
	sembrar(3, "open", since.Add(-24*time.Hour), time.Time{}, false)             // abierto: fuera
	sembrar(4, "closed", since.Add(-200*time.Hour), since.Add(-time.Hour), true) // merged antes de since: fuera

	revSlow, err := st.CreateReview(ctx, CreateReviewParams{PullRequestID: prSlow.ID, HeadSha: "hs", BaseSha: "bs"})
	if err != nil {
		t.Fatal(err)
	}
	revFast, err := st.CreateReview(ctx, CreateReviewParams{PullRequestID: prFast.ID, HeadSha: "hf", BaseSha: "bf"})
	if err != nil {
		t.Fatal(err)
	}

	// Findings: 1 aceptado, 1 rechazado, 1 sin evaluar → evaluados 2, aceptados 1.
	fAcc := f5Finding(t, st, revSlow.ID, "f5-sum-acc")
	fRej := f5Finding(t, st, revSlow.ID, "f5-sum-rej")
	f5Finding(t, st, revSlow.ID, "f5-sum-nil")
	if err := st.UpdateFindingAccepted(ctx, UpdateFindingAcceptedParams{ID: fAcc.ID, Accepted: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateFindingAccepted(ctx, UpdateFindingAcceptedParams{ID: fRej.ID, Accepted: pgtype.Bool{Bool: false, Valid: true}}); err != nil {
		t.Fatal(err)
	}

	// Comments inline: resolved∧applied=false (FP), resolved∧applied=true,
	// resolved=false (thread abierto), y un summary sin outcome.
	csFP := f5Inline(t, st, prSlow.ID, revSlow.ID, "gh-f5-fp")
	if err := st.UpdateCommentsSentResolved(ctx, UpdateCommentsSentResolvedParams{ID: csFP.ID, Resolved: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCommentsSentApplied(ctx, UpdateCommentsSentAppliedParams{ID: csFP.ID, Applied: pgtype.Bool{Bool: false, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	csOK := f5Inline(t, st, prSlow.ID, revSlow.ID, "gh-f5-ok")
	if err := st.UpdateCommentsSentResolved(ctx, UpdateCommentsSentResolvedParams{ID: csOK.ID, Resolved: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCommentsSentApplied(ctx, UpdateCommentsSentAppliedParams{ID: csOK.ID, Applied: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	csOpen := f5Inline(t, st, prSlow.ID, revSlow.ID, "gh-f5-open")
	if err := st.UpdateCommentsSentResolved(ctx, UpdateCommentsSentResolvedParams{ID: csOpen.ID, Resolved: pgtype.Bool{Bool: false, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateCommentSent(ctx, CreateCommentSentParams{
		PullRequestID: prSlow.ID, ReviewID: pgtype.Int8{Int64: revSlow.ID, Valid: true},
		CommentID: "gh-f5-summary", Type: "summary",
	}); err != nil {
		t.Fatal(err)
	}

	// Usage con linkage: revSlow acumula 45 tokens (2 llamadas), revFast 10
	// (1 llamada) → promedio por review 27.5; la fila sin review no cuenta.
	uso := func(revID int64, in, out int32) {
		t.Helper()
		if _, err := st.CreateLlmUsage(ctx, CreateLlmUsageParams{
			ReviewID: pgtype.Int8{Int64: revID, Valid: revID > 0},
			Role:     "review", Provider: "stub", Model: "stub-model",
			TokensIn: in, TokensOut: out,
		}); err != nil {
			t.Fatalf("CreateLlmUsage: %v", err)
		}
	}
	uso(revSlow.ID, 10, 5)
	uso(revSlow.ID, 20, 10)
	uso(revFast.ID, 7, 3)
	uso(0, 99, 99) // fuera de corrida: review_id null

	// Limpieza de las hojas: la BD es compartida — sin esto, una re-corrida
	// acumularía findings/comments/usage viejos. PRs/reviews/repos quedan
	// (precedente f1). Con la ventana de GetAvgLlmTokensPerReview (T7) el
	// promedio es exacto: solo nuestros usos son posteriores a `since`.
	t.Cleanup(func() {
		revIDs := []int64{revSlow.ID, revFast.ID}
		prIDs := []int64{prSlow.ID, prFast.ID}
		if _, err := st.Pool.Exec(ctx, "DELETE FROM llm_usage WHERE review_id = ANY($1)", revIDs); err != nil {
			t.Errorf("limpiando llm_usage: %v", err)
		}
		if _, err := st.Pool.Exec(ctx, "DELETE FROM findings WHERE review_id = ANY($1)", revIDs); err != nil {
			t.Errorf("limpiando findings: %v", err)
		}
		if _, err := st.Pool.Exec(ctx, "DELETE FROM comments_sent WHERE pull_request_id = ANY($1)", prIDs); err != nil {
			t.Errorf("limpiando comments_sent: %v", err)
		}
	})

	sum, err := st.GetMetricsSummary(ctx, pgtype.Timestamptz{Time: since, Valid: true})
	if err != nil {
		t.Fatalf("GetMetricsSummary: %v", err)
	}
	if sum.MergedPrs != 2 {
		t.Errorf("MergedPrs = %d, querés 2 (abierto y pre-window afuera)", sum.MergedPrs)
	}
	wantCycle := (mergedAt.Sub(since.Add(-48*time.Hour)).Hours() + mergedAt.Sub(since.Add(-12*time.Hour)).Hours()) / 2
	if math.Abs(sum.AvgCycleTimeHours-wantCycle) > 0.001 {
		t.Errorf("AvgCycleTimeHours = %v, querés %v (30h1s ± truncado µs)", sum.AvgCycleTimeHours, wantCycle)
	}
	if sum.AcceptedFindings != 1 || sum.EvaluatedFindings != 2 {
		t.Errorf("aceptados: got %d/%d, querés 1 aceptado de 2 evaluados", sum.AcceptedFindings, sum.EvaluatedFindings)
	}
	if sum.ResolvedComments != 2 || sum.FalsePositives != 1 {
		t.Errorf("FP: got %d FP de %d resueltos, querés 1 de 2 (resolved∧¬applied)", sum.FalsePositives, sum.ResolvedComments)
	}

	tok, err := st.GetAvgLlmTokensPerReview(ctx, pgtype.Timestamptz{Time: since, Valid: true})
	if err != nil {
		t.Fatalf("GetAvgLlmTokensPerReview: %v", err)
	}
	// La ventana (T7) deja fuera todo lo anterior a `since`: nuestros dos
	// grupos (45 y 10 tokens) son exactos, sin contaminación de la BD
	// compartida.
	if tok.ReviewsCounted != 2 {
		t.Errorf("ReviewsCounted = %d, querés exactamente 2", tok.ReviewsCounted)
	}
	if math.Abs(tok.AvgTokensPerReview-27.5) > 0.001 {
		t.Errorf("AvgTokensPerReview = %v, querés 27.5 ((45+10)/2)", tok.AvgTokensPerReview)
	}
}
