//go:build integration

// E2E de trazabilidad de merges sin PR (trazabilidad-merges S2/S5): el
// PushReviewJob analiza un push directo a la rama base con el pipeline
// completo — SAST + agentes LLM + resumen/walkthrough/mermaid — y la salida
// vive SOLO en el dashboard (S5): fila en reviews con direct_push_id,
// findings, summary, y CERO publicación en el VCS (sin comments_sent). Un
// push más nuevo del mismo repo marca la corrida stale sin gastar LLM.
// Requiere DATABASE_URL (Postgres de desarrollo) — sin ella se salta, igual
// que el resto de los tests de integración.
package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// pushAnalyzer sirve un hallazgo SAST sobre main.go:5 — línea de contexto
// del diff fixture: el hallazgo ancla al diff del push y persiste igual que
// en un PR.
type pushAnalyzer struct{}

func (pushAnalyzer) Run(context.Context, string) (*analyze.AnalysisResult, error) {
	return &analyze.AnalysisResult{
		FilesAnalyzed: 1,
		LintersRun:    []analyze.LinterStatus{{Name: "stub", Status: "ok"}},
		Findings: []analyze.Finding{{
			File: "main.go", Line: 5, Severity: "low", Category: "style",
			Body: "saludo sin usar", Source: "sast", Linter: "revive", Rule: "naming",
		}},
	}, nil
}

// pushSeedRepo arma el entorno del E2E: repo efímero + proveedor LLM stub
// del rol review. Los pushes, reviews y findings se van con el repo
// (DeleteRepositoryCascade); el proveedor y su uso se limpian aparte.
func pushSeedRepo(t *testing.T, st *store.Store, masterKey []byte) (store.Repository, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixNano()
	repo, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: now,
		Owner:      "test",
		Name:       fmt.Sprintf("repo-push-%d", now),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	t.Cleanup(func() {
		if err := st.DeleteRepositoryCascade(ctx, repo.ID); err != nil {
			t.Errorf("borrando el repo de test: %v", err)
		}
	})

	llmSrv := stubLLM(t, reviewerOut, summarizerOut)
	encKey, err := store.Encrypt(masterKey, []byte("sk-push-test"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	provider, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: llmSrv.URL, Model: "stub-model",
		ApiKey: encKey, Role: "review", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(ctx, "DELETE FROM llm_usage WHERE provider = $1", llmSrv.URL)
		if err := st.DeleteLlmProvider(ctx, provider.ID); err != nil {
			t.Errorf("borrando el proveedor stub: %v", err)
		}
	})
	return repo, llmSrv.URL
}

// pushJob arma el Job River en memoria que el worker consume — el job nunca
// pasa por river_job: el worker vivo del `just dev` comparte la BD y no
// puede robar lo que no se encola (mismo patrón que los tests del ReviewJob).
func pushJob(args jobs.PushReviewJobArgs) *river.Job[jobs.PushReviewJobArgs] {
	return &river.Job[jobs.PushReviewJobArgs]{
		JobRow: &rivertype.JobRow{ID: time.Now().UnixNano(), Attempt: 1, MaxAttempts: 5},
		Args:   args,
	}
}

// TestPushReviewEndToEnd (S2/S5): el worker corre el pipeline completo sobre
// un push directo — SAST + reviewer + summarizer con mermaid — y persiste
// todo en la fila colgada del push SIN publicar nada en el VCS.
func TestPushReviewEndToEnd(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	masterKey := make([]byte, 32)
	repo, _ := pushSeedRepo(t, st, masterKey)

	// El "clon" del job: el head del push es el SHA real del clon, la base
	// es fake (el merge-base cae a base_sha, §3.6.1) — el worker arma su
	// propio workdir del clon de la rama base (stub no-op).
	_, headSHA := newTempClone(t)
	push, err := st.UpsertDirectPush(ctx, store.UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: fakeBase, AfterSha: headSHA, Committer: "dev-integration",
	})
	if err != nil {
		t.Fatalf("UpsertDirectPush: %v", err)
	}

	gateway := llm.New(st, masterKey, llm.Limits{
		MaxPerReview: 8, MaxGlobal: 8, Timeout: 10 * time.Second, MaxRetries: 1,
	})
	// GetCommitDiff del stub sirve el mismo diff fixture (main.go con el
	// import nuevo): el reviewer ancla su hallazgo a main.go:3.
	vcsStub := &stubVCS{diff: diffFixture}
	w := &jobs.PushReviewWorker{
		Store: st, Gateway: gateway, Analyzer: pushAnalyzer{}, Provider: vcsStub,
		Queue: newCapturedQueue(), Config: review.DefaultConfig(), WorkdirRoot: t.TempDir(),
	}
	if err := w.Work(ctx, pushJob(jobs.PushReviewJobArgs{RepositoryID: repo.ID, PushID: push.ID})); err != nil {
		t.Fatalf("Work: %v", err)
	}

	// Fila en reviews colgada del push: success, identidad after..before,
	// resumen y walkthrough del summarizer.
	var revID int64
	var status, summary, walkthrough, head, base string
	if err := st.Pool.QueryRow(ctx,
		`SELECT id, status, summary, walkthrough, head_sha, base_sha FROM reviews WHERE direct_push_id = $1`,
		push.ID).Scan(&revID, &status, &summary, &walkthrough, &head, &base); err != nil {
		t.Fatalf("la review del push debe existir: %v", err)
	}
	if status != review.StatusSuccess {
		t.Errorf("reviews.status = %q, querés success", status)
	}
	if summary != summaryTxt || walkthrough == "" {
		t.Errorf("summary/walkthrough = %q / %q, querés %q / no vacío", summary, walkthrough, summaryTxt)
	}
	if head != push.AfterSha || base != push.BeforeSha {
		t.Errorf("identidad = %s..%s, querés %s..%s", base, head, push.BeforeSha, push.AfterSha)
	}

	// Findings: 1 LLM (reviewer, main.go:3) + 1 SAST (main.go:5) — el
	// pipeline de análisis es el mismo que el de un PR (S2).
	findings, err := st.ListFindingsByReview(ctx, revID)
	if err != nil {
		t.Fatalf("ListFindingsByReview: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, querés 2 (LLM + SAST): %+v", len(findings), findings)
	}
	porFuente := map[string]store.Finding{}
	for _, f := range findings {
		porFuente[f.Source] = f
	}
	if f := porFuente[review.SourceLLM]; f.File != "main.go" || f.Line != 3 || f.Category != "logic" {
		t.Errorf("finding LLM = %+v, querés main.go:3 logic", f)
	}
	if f := porFuente[review.SourceSAST]; f.File != "main.go" || f.Line != 5 {
		t.Errorf("finding SAST = %+v, querés main.go:5", f)
	}

	// S5: CERO publicación — ni comments_sent ni cuerpos en el VCS stub.
	var comentarios int
	if err := st.Pool.QueryRow(ctx,
		"SELECT COUNT(1) FROM comments_sent WHERE review_id = $1", revID).Scan(&comentarios); err != nil {
		t.Fatalf("contando comments_sent: %v", err)
	}
	if comentarios != 0 {
		t.Errorf("comments_sent = %d, querés 0 (S5: salida solo dashboard)", comentarios)
	}
	if summaries, inlines := vcsStub.snapshot(); len(summaries) != 0 || len(inlines) != 0 {
		t.Errorf("el VCS stub recibió publicación: %d resúmenes, %d inlines", len(summaries), len(inlines))
	}
}

// TestPushReviewStaleEndToEnd (S2): cuando ya existe un push más nuevo del
// mismo repo (after distinto, creado después), la corrida del push viejo
// queda stale SIN gastar LLM — cero uso del proveedor stub.
func TestPushReviewStaleEndToEnd(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	masterKey := make([]byte, 32)
	repo, stubURL := pushSeedRepo(t, st, masterKey)

	// El clon solo fija el SHA real del head del push viejo (la base es fake
	// — el merge-base cae a base_sha, §3.6.1).
	_, headSHA := newTempClone(t)
	push1, err := st.UpsertDirectPush(ctx, store.UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: fakeBase, AfterSha: headSHA, Committer: "dev-integration",
	})
	if err != nil {
		t.Fatalf("UpsertDirectPush: %v", err)
	}
	// El push más nuevo aterriza ANTES de que corra el job del viejo.
	if _, err := st.UpsertDirectPush(ctx, store.UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: headSHA, AfterSha: syncHead, Committer: "dev-integration",
	}); err != nil {
		t.Fatalf("UpsertDirectPush del push nuevo: %v", err)
	}

	gateway := llm.New(st, masterKey, llm.Limits{
		MaxPerReview: 8, MaxGlobal: 8, Timeout: 10 * time.Second, MaxRetries: 1,
	})
	vcsStub := &stubVCS{diff: diffFixture}
	w := &jobs.PushReviewWorker{
		Store: st, Gateway: gateway, Analyzer: pushAnalyzer{}, Provider: vcsStub,
		Queue: newCapturedQueue(), Config: review.DefaultConfig(), WorkdirRoot: t.TempDir(),
	}
	if err := w.Work(ctx, pushJob(jobs.PushReviewJobArgs{RepositoryID: repo.ID, PushID: push1.ID})); err != nil {
		t.Fatalf("Work: %v", err)
	}

	var status, summary string
	if err := st.Pool.QueryRow(ctx,
		`SELECT status, summary FROM reviews WHERE direct_push_id = $1`, push1.ID).Scan(&status, &summary); err != nil {
		t.Fatalf("la review del push viejo debe existir: %v", err)
	}
	if status != review.StatusStale {
		t.Errorf("reviews.status = %q, querés stale (hay un push más nuevo)", status)
	}
	if summary != "" {
		t.Errorf("summary = %q, querés vacío (stale no corre summarizer)", summary)
	}

	// Sin gasto de LLM: cero uso del proveedor stub.
	var uso int
	if err := st.Pool.QueryRow(ctx,
		"SELECT COUNT(1) FROM llm_usage WHERE provider = $1", stubURL).Scan(&uso); err != nil {
		t.Fatalf("contando llm_usage: %v", err)
	}
	if uso != 0 {
		t.Errorf("llm_usage = %d filas, querés 0 (stale no gasta LLM)", uso)
	}
}
