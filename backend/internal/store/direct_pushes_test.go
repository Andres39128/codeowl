package store

// Tests de direct_pushes y reviews polimórficas (trazabilidad-merges S6):
// idempotencia del upsert por (repo, after_sha), target CHECK (exactamente
// uno de pull_request_id / direct_push_id) y el listado con el estado de la
// corrida más reciente.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// seedPushRepo crea un repo fresco que se borra en cascada al terminar.
func seedPushRepo(t *testing.T, st *Store) Repository {
	t.Helper()
	repo, err := st.CreateRepository(context.Background(), CreateRepositoryParams{
		Vcs: "github", ExternalID: time.Now().UnixNano()%1_000_000 + 1,
		Owner: "acme", Name: "push-store-test",
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	t.Cleanup(func() {
		if err := st.DeleteRepositoryCascade(context.Background(), repo.ID); err != nil {
			t.Errorf("DeleteRepositoryCascade: %v", err)
		}
	})
	return repo
}

func TestDirectPushUpsertIdempotente(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := seedPushRepo(t, st)

	push, err := st.UpsertDirectPush(ctx, UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: "b-1", AfterSha: "a-1", Committer: "ana",
	})
	if err != nil {
		t.Fatalf("UpsertDirectPush: %v", err)
	}

	// Reintento del webhook (mismo after_sha): reusa la fila y refresca.
	dup, err := st.UpsertDirectPush(ctx, UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: "b-1", AfterSha: "a-1", Committer: "bia",
	})
	if err != nil {
		t.Fatalf("UpsertDirectPush (dup): %v", err)
	}
	if dup.ID != push.ID {
		t.Errorf("reintento del mismo push: id %d, querés el original %d", dup.ID, push.ID)
	}
	if dup.Committer != "bia" {
		t.Errorf("reintento debe refrescar committer: %q, querés \"bia\"", dup.Committer)
	}

	// Otro after_sha del mismo repo: fila nueva.
	otro, err := st.UpsertDirectPush(ctx, UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: "a-1", AfterSha: "a-2", Committer: "ana",
	})
	if err != nil {
		t.Fatalf("UpsertDirectPush (nuevo): %v", err)
	}
	if otro.ID == push.ID {
		t.Error("un after_sha distinto es un push distinto")
	}
}

func TestReviewTargetPolimorfico(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := seedPushRepo(t, st)

	push, err := st.UpsertDirectPush(ctx, UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: "b-1", AfterSha: "a-1", Committer: "ana",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Corrida del push (target direct_push_id, sin PR): head=after, base=before.
	rev, err := st.CreateReview(ctx, CreateReviewParams{
		DirectPushID: pgtype.Int8{Int64: push.ID, Valid: true},
		HeadSha:      "a-1",
		BaseSha:      "b-1",
	})
	if err != nil {
		t.Fatalf("CreateReview (push): %v", err)
	}
	if rev.PullRequestID.Valid {
		t.Error("corrida de push no debe tener pull_request_id")
	}

	// CHECK (migración 0011): sin target → error, no fila huérfana.
	if _, err := st.CreateReview(ctx, CreateReviewParams{HeadSha: "x", BaseSha: "y"}); err == nil {
		t.Error("review sin target debe violar reviews_target_check")
	}

	// Listado: trae repo y estado de la corrida más reciente.
	rows, err := st.ListDirectPushes(ctx, pgtype.Int8{Int64: repo.ID, Valid: true})
	if err != nil {
		t.Fatalf("ListDirectPushes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListDirectPushes = %d filas, querés 1", len(rows))
	}
	row := rows[0]
	if row.RepoOwner != "acme" || row.RepoName != "push-store-test" {
		t.Errorf("listado debe traer owner/name: %s/%s", row.RepoOwner, row.RepoName)
	}
	if !row.LatestReviewID.Valid || row.LatestReviewID.Int64 != rev.ID {
		t.Errorf("latest_review_id = %+v, querés %d", row.LatestReviewID, rev.ID)
	}
	if !row.LatestReviewStatus.Valid || row.LatestReviewStatus.String != "running" {
		t.Errorf("latest_review_status = %+v, querés running (nace running)", row.LatestReviewStatus)
	}

	// Filtro null: todos los repos (≥ esta fila).
	todos, err := st.ListDirectPushes(ctx, pgtype.Int8{})
	if err != nil {
		t.Fatalf("ListDirectPushes (todos): %v", err)
	}
	if len(todos) < 1 {
		t.Error("repository_id null debe listar todos los repos")
	}
}

// T4: el listado es UNA fila por push con SU corrida más reciente — el JOIN
// con reviews matchea solo la última (id DESC, mismo criterio que
// GetLatestReviewByPR), no una fila por corrida; y trae el vcs del repo
// para la vista de trazabilidad del dashboard.
func TestListDirectPushesUltimaReviewSinDuplicar(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := seedPushRepo(t, st)

	push, err := st.UpsertDirectPush(ctx, UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main",
		BeforeSha: "b-9", AfterSha: "a-9", Committer: "ana",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Dos corridas: la vieja queda stale, la nueva (id mayor) nace running.
	r1, err := st.CreateReview(ctx, CreateReviewParams{
		DirectPushID: pgtype.Int8{Int64: push.ID, Valid: true}, HeadSha: "a-9", BaseSha: "b-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateReviewStatus(ctx, UpdateReviewStatusParams{ID: r1.ID, Status: "stale"}); err != nil {
		t.Fatal(err)
	}
	r2, err := st.CreateReview(ctx, CreateReviewParams{
		DirectPushID: pgtype.Int8{Int64: push.ID, Valid: true}, HeadSha: "a-9", BaseSha: "b-9",
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := st.ListDirectPushes(ctx, pgtype.Int8{Int64: repo.ID, Valid: true})
	if err != nil {
		t.Fatalf("ListDirectPushes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListDirectPushes = %d filas, querés 1 (una por push, no por corrida): %+v", len(rows), rows)
	}
	row := rows[0]
	if !row.LatestReviewID.Valid || row.LatestReviewID.Int64 != r2.ID {
		t.Errorf("latest_review_id = %+v, querés la corrida más reciente %d", row.LatestReviewID, r2.ID)
	}
	if !row.LatestReviewStatus.Valid || row.LatestReviewStatus.String != "running" {
		t.Errorf("latest_review_status = %+v, querés running (r2, no el stale de r1)", row.LatestReviewStatus)
	}
	if row.RepoVcs != "github" {
		t.Errorf("repo_vcs = %q, querés github", row.RepoVcs)
	}
}
