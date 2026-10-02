package store

// Tests F1 de las tablas núcleo (guía §3.3): round-trips de CRUD, orden de
// failover de proveedores, upsert idempotente de PRs, dedup de webhooks y
// conjuntos cerrados de findings. Corren contra el Postgres de desarrollo vía
// testStore (store_test.go); los valores usan sufijos únicos por corrida para
// no chocar entre tests ni entre ejecuciones.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// f1Nano da un valor único por corrida para las claves naturales.
func f1Nano() int64 { return time.Now().UnixNano() }

func f1Text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// f1Repo crea un repositorio GitHub efímero para colgar PRs y reviews.
func f1Repo(t *testing.T, st *Store) Repository {
	t.Helper()
	repo, err := st.CreateRepository(context.Background(), CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: f1Nano(),
		Owner:      "codeowl-tests",
		Name:       fmt.Sprintf("repo-%d", f1Nano()),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	return repo
}

// f1PR crea un PR abierto colgado de repo; created_at es la fecha del PR en
// el VCS (un día atrás), no la del registro local.
func f1PR(t *testing.T, st *Store, repoID, number int64) PullRequest {
	t.Helper()
	pr, err := st.UpsertPullRequest(context.Background(), UpsertPullRequestParams{
		RepositoryID: repoID,
		Number:       number,
		Author:       "autora",
		State:        "open",
		HeadSha:      fmt.Sprintf("head-%d", number),
		BaseRef:      "main",
		BaseSha:      "basesha",
		CreatedAt:    pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}
	return pr
}

func TestLlmProvidersCRUDYOrdenFailover(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	crear := func(model string, priority int32, enabled bool) LlmProvider {
		p, err := st.CreateLlmProvider(ctx, CreateLlmProviderParams{
			BaseUrl:  "https://api.proveedor.test/v1",
			Model:    fmt.Sprintf("%s-%d", model, f1Nano()),
			ApiKey:   "ciphertext-aes-gcm",
			Role:     "review",
			Priority: priority,
			Enabled:  enabled,
		})
		if err != nil {
			t.Fatalf("CreateLlmProvider(%s): %v", model, err)
		}
		return p
	}
	pAlta := crear("alta", 1, true)
	pEmpate := crear("empate", 1, true)
	pBaja := crear("baja", 2, true)
	defer func() {
		_, _ = st.Pool.Exec(ctx, "DELETE FROM llm_providers WHERE id = ANY($1)", []int64{pAlta.ID, pEmpate.ID, pBaja.ID})
	}()

	// Orden de failover: priority asc, empate por id asc — orden estable (§3.3).
	lista, err := st.ListEnabledLlmProvidersByRole(ctx, "review")
	if err != nil {
		t.Fatalf("ListEnabledLlmProvidersByRole: %v", err)
	}
	pos := func(p LlmProvider) int {
		for i, row := range lista {
			if row.ID == p.ID {
				return i
			}
		}
		t.Fatalf("el proveedor %d no aparece en el listado del rol", p.ID)
		return -1
	}
	if !(pos(pAlta) < pos(pEmpate) && pos(pEmpate) < pos(pBaja)) {
		t.Errorf("el orden de failover debe ser priority asc, id asc: alta=%d empate=%d baja=%d", pos(pAlta), pos(pEmpate), pos(pBaja))
	}

	// Round-trip de lectura.
	got, err := st.GetLlmProvider(ctx, pBaja.ID)
	if err != nil {
		t.Fatalf("GetLlmProvider: %v", err)
	}
	if got.BaseUrl != pBaja.BaseUrl || got.Model != pBaja.Model || got.ApiKey != "ciphertext-aes-gcm" || got.Role != "review" || got.Priority != 2 || !got.Enabled {
		t.Errorf("round-trip del proveedor: got %+v", got)
	}

	// Update: cambia modelo y prioridad.
	upd, err := st.UpdateLlmProvider(ctx, UpdateLlmProviderParams{
		ID:       pBaja.ID,
		BaseUrl:  got.BaseUrl,
		Model:    "modelo-editado",
		ApiKey:   got.ApiKey,
		Role:     got.Role,
		Priority: 9,
		Enabled:  got.Enabled,
	})
	if err != nil {
		t.Fatalf("UpdateLlmProvider: %v", err)
	}
	if upd.Model != "modelo-editado" || upd.Priority != 9 {
		t.Errorf("UpdateLlmProvider no aplicó los cambios: %+v", upd)
	}

	// Los deshabilitados salen de la cadena de failover del rol.
	if _, err := st.UpdateLlmProvider(ctx, UpdateLlmProviderParams{
		ID: pEmpate.ID, BaseUrl: pEmpate.BaseUrl, Model: pEmpate.Model,
		ApiKey: pEmpate.ApiKey, Role: pEmpate.Role, Priority: pEmpate.Priority, Enabled: false,
	}); err != nil {
		t.Fatalf("deshabilitando pEmpate: %v", err)
	}
	lista, err = st.ListEnabledLlmProvidersByRole(ctx, "review")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range lista {
		if row.ID == pEmpate.ID {
			t.Error("un proveedor deshabilitado no debe aparecer en el rol")
		}
	}

	// Delete: después no se resuelve.
	if err := st.DeleteLlmProvider(ctx, pAlta.ID); err != nil {
		t.Fatalf("DeleteLlmProvider: %v", err)
	}
	if _, err := st.GetLlmProvider(ctx, pAlta.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("el proveedor borrado no debe resolverse, got %v", err)
	}
}

func TestRepositoriesCRUD(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// GitHub: sin secretos por repo (el secret de webhook es global, §9.3).
	repo, err := st.CreateRepository(ctx, CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: f1Nano(),
		Owner:      "codeowl-tests",
		Name:       "sin-secretos",
	})
	if err != nil {
		t.Fatalf("CreateRepository github: %v", err)
	}
	if !repo.Enabled || repo.ReviewDrafts || repo.Language != "es" || !repo.ChatOrgOnly {
		t.Errorf("defaults de config: got %+v", repo)
	}

	// Clave natural (vcs, external_id).
	byVCS, err := st.GetRepositoryByVCSExternalID(ctx, GetRepositoryByVCSExternalIDParams{Vcs: repo.Vcs, ExternalID: repo.ExternalID})
	if err != nil {
		t.Fatalf("GetRepositoryByVCSExternalID: %v", err)
	}
	if byVCS.ID != repo.ID {
		t.Errorf("GetRepositoryByVCSExternalID devolvió otro repo: %+v", byVCS)
	}

	// Unicidad (vcs, external_id): el mismo repo no se registra dos veces.
	if _, err := st.CreateRepository(ctx, CreateRepositoryParams{
		Vcs: repo.Vcs, ExternalID: repo.ExternalID, Owner: repo.Owner, Name: repo.Name,
	}); err == nil {
		t.Error("el par (vcs, external_id) debe ser único")
	}

	// GitLab: secretos por repo, cifrados en la app (§9.2/§9.3) — la BD solo
	// ve texto cifrado; base_url vacío = gitlab.com.
	gl, err := st.CreateRepository(ctx, CreateRepositoryParams{
		Vcs:           "gitlab",
		ExternalID:    f1Nano(),
		Owner:         "codeowl-tests",
		Name:          "con-secretos",
		WebhookSecret: f1Text("signing-token-cifrado"),
		SecretToken:   f1Text("secret-token-legacy-cifrado"),
		ApiToken:      f1Text("project-token-cifrado"),
		DeployKey:     f1Text("deploy-key-cifrada"),
		BaseUrl:       f1Text("https://gitlab.example.com"),
	})
	if err != nil {
		t.Fatalf("CreateRepository gitlab: %v", err)
	}
	glGot, err := st.GetRepository(ctx, gl.ID)
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if !glGot.WebhookSecret.Valid || glGot.WebhookSecret.String != "signing-token-cifrado" ||
		!glGot.ApiToken.Valid || glGot.ApiToken.String != "project-token-cifrado" ||
		!glGot.DeployKey.Valid || glGot.DeployKey.String != "deploy-key-cifrada" ||
		!glGot.BaseUrl.Valid || glGot.BaseUrl.String != "https://gitlab.example.com" {
		t.Errorf("round-trip de secretos GitLab: got %+v", glGot)
	}

	// Update de config: idioma y drafts; los secretos no se pierden.
	glUpd, err := st.UpdateRepository(ctx, UpdateRepositoryParams{
		ID: gl.ID, Owner: gl.Owner, Name: gl.Name,
		WebhookSecret: gl.WebhookSecret, SecretToken: gl.SecretToken,
		ApiToken: gl.ApiToken, BaseUrl: gl.BaseUrl, DeployKey: gl.DeployKey,
		ReviewDrafts: true, Language: "en", ChatOrgOnly: gl.ChatOrgOnly,
	})
	if err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}
	if !glUpd.ReviewDrafts || glUpd.Language != "en" || !glUpd.ApiToken.Valid {
		t.Errorf("UpdateRepository: got %+v", glUpd)
	}

	// Desconexión como flag, sin delete (§3.5).
	off, err := st.SetRepositoryEnabled(ctx, SetRepositoryEnabledParams{ID: repo.ID, Enabled: false})
	if err != nil {
		t.Fatalf("SetRepositoryEnabled: %v", err)
	}
	if off.Enabled {
		t.Error("SetRepositoryEnabled(false) debe apagar el flag")
	}
}

func TestPullRequestsUpsertIdempotente(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := f1Repo(t, st)

	vcsDate := time.Now().Add(-72 * time.Hour)
	primero, err := st.UpsertPullRequest(ctx, UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 7, Author: "autora", State: "open",
		HeadSha: "h1", BaseRef: "main", BaseSha: "b1",
		MergedAt:  pgtype.Timestamptz{},
		CreatedAt: pgtype.Timestamptz{Time: vcsDate, Valid: true},
	})
	if err != nil {
		t.Fatalf("primer upsert: %v", err)
	}

	// Re-procesar el webhook (nuevo head): misma fila, datos actualizados.
	segundo, err := st.UpsertPullRequest(ctx, UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 7, Author: "autora", State: "open",
		HeadSha: "h2", BaseRef: "main", BaseSha: "b2",
		MergedAt:  pgtype.Timestamptz{},
		CreatedAt: pgtype.Timestamptz{Time: vcsDate.Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("segundo upsert: %v", err)
	}
	if segundo.ID != primero.ID {
		t.Fatalf("el upsert debe ser idempotente: ids %d y %d", primero.ID, segundo.ID)
	}
	if segundo.HeadSha != "h2" || segundo.BaseSha != "b2" {
		t.Errorf("el upsert debe actualizar los SHAs: got %+v", segundo)
	}
	// timestamptz tiene precisión de microsegundos: comparar truncado.
	if !segundo.CreatedAt.Time.Truncate(time.Microsecond).Equal(vcsDate.Truncate(time.Microsecond)) {
		t.Errorf("created_at conserva la fecha del PR en el VCS: got %v want %v", segundo.CreatedAt.Time, vcsDate)
	}

	// GetPullRequestByRepoNumber resuelve por la clave natural.
	got, err := st.GetPullRequestByRepoNumber(ctx, GetPullRequestByRepoNumberParams{RepositoryID: repo.ID, Number: 7})
	if err != nil {
		t.Fatalf("GetPullRequestByRepoNumber: %v", err)
	}
	if got.ID != primero.ID {
		t.Errorf("GetPullRequestByRepoNumber: got id %d want %d", got.ID, primero.ID)
	}

	// Merge: close con merged_at (§3.3).
	mergeAt := time.Now()
	merged, err := st.UpdatePullRequestState(ctx, UpdatePullRequestStateParams{
		ID: primero.ID, State: "closed", MergedAt: pgtype.Timestamptz{Time: mergeAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("UpdatePullRequestState: %v", err)
	}
	if merged.State != "closed" || !merged.MergedAt.Valid {
		t.Errorf("merge: got %+v", merged)
	}

	// Un evento posterior sin merge no borra el registro (COALESCE).
	tarde, err := st.UpsertPullRequest(ctx, UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 7, Author: "autora", State: "closed",
		HeadSha: "h3", BaseRef: "main", BaseSha: "b3",
		MergedAt:  pgtype.Timestamptz{},
		CreatedAt: pgtype.Timestamptz{Time: vcsDate, Valid: true},
	})
	if err != nil {
		t.Fatalf("upsert post-merge: %v", err)
	}
	if !tarde.MergedAt.Valid {
		t.Error("merged_at no debe perderse en upserts posteriores sin merge")
	}

	// Cerrado: fuera de los abiertos del repo.
	abiertos, err := st.ListOpenPullRequestsByRepo(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range abiertos {
		if pr.ID == primero.ID {
			t.Error("un PR cerrado no debe listar como abierto")
		}
	}
}

func TestReviewsYFindings(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := f1Repo(t, st)
	pr := f1PR(t, st, repo.ID, 11)

	// La corrida nace running con textos vacíos; head/base son su identidad.
	rev, err := st.CreateReview(ctx, CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h11", BaseSha: "b11"})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	if rev.Status != "running" || rev.Summary != "" || rev.Walkthrough != "" || rev.Mermaid != "" {
		t.Errorf("la review debe nacer running y vacía: got %+v", rev)
	}

	// Cierre de la corrida: textos + estado.
	if _, err := st.UpdateReviewSummary(ctx, UpdateReviewSummaryParams{
		ID: rev.ID, Summary: "resumen", Walkthrough: "walkthrough", Mermaid: "graph TD",
	}); err != nil {
		t.Fatalf("UpdateReviewSummary: %v", err)
	}
	final, err := st.UpdateReviewStatus(ctx, UpdateReviewStatusParams{ID: rev.ID, Status: "success"})
	if err != nil {
		t.Fatalf("UpdateReviewStatus: %v", err)
	}
	if final.Status != "success" || final.Summary != "resumen" {
		t.Errorf("cierre de la corrida: got %+v", final)
	}
	// Conjunto cerrado de estados.
	if _, err := st.UpdateReviewStatus(ctx, UpdateReviewStatusParams{ID: rev.ID, Status: "ok"}); err == nil {
		t.Error("status 'ok' debe rechazarse (conjunto cerrado §3.3)")
	}

	// Findings: llm nace sin verificar; sast se publica sin verificación.
	fLLM, err := st.CreateFinding(ctx, CreateFindingParams{
		ReviewID: rev.ID, File: "main.go", Line: 42,
		Severity: "high", Category: "security", Body: "inyección",
		Suggestion: f1Text("usar queries parametrizadas"), Source: "llm",
		Verified: pgtype.Bool{}, // null hasta que el Verifier corre (F3)
	})
	if err != nil {
		t.Fatalf("CreateFinding llm: %v", err)
	}
	if fLLM.Verified.Valid {
		t.Error("un finding llm debe nacer verified=null")
	}
	fSAST, err := st.CreateFinding(ctx, CreateFindingParams{
		ReviewID: rev.ID, File: "a.py", Line: 3,
		Severity: "low", Category: "style", Body: "lint", Source: "sast",
		Verified: pgtype.Bool{Bool: false, Valid: false},
	})
	if err != nil {
		t.Fatalf("CreateFinding sast: %v", err)
	}
	hallazgos, err := st.ListFindingsByReview(ctx, rev.ID)
	if err != nil || len(hallazgos) != 2 {
		t.Fatalf("ListFindingsByReview: n=%d err=%v", len(hallazgos), err)
	}

	// Conjuntos cerrados de severity/category/source: el CHECK rechaza de más.
	for _, tc := range []struct{ sev, cat, src string }{
		{"critica", "security", "llm"},  // severity fuera del conjunto
		{"high", "arquitectura", "llm"}, // category fuera del conjunto
		{"high", "security", "humano"},  // source fuera del conjunto
	} {
		if _, err := st.CreateFinding(ctx, CreateFindingParams{
			ReviewID: rev.ID, File: "x.go", Line: 1,
			Severity: tc.sev, Category: tc.cat, Body: "b", Source: tc.src,
		}); err == nil {
			t.Errorf("el CHECK debe rechazar severity=%q category=%q source=%q", tc.sev, tc.cat, tc.src)
		}
	}

	// Segunda corrida del PR: GetLatestByPR apunta a la última.
	rev2, err := st.CreateReview(ctx, CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h12", BaseSha: "b12"})
	if err != nil {
		t.Fatalf("CreateReview 2: %v", err)
	}
	latest, err := st.GetLatestReviewByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetLatestReviewByPR: %v", err)
	}
	if latest.ID != rev2.ID {
		t.Errorf("GetLatestReviewByPR: got %d want %d", latest.ID, rev2.ID)
	}

	// ListFindingsByPR cruza todas las corridas del PR.
	porPR, err := st.ListFindingsByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("ListFindingsByPR: %v", err)
	}
	// fLLM se creó primero: id menor → primero en el ORDER BY f.id.
	if len(porPR) != 2 || porPR[0].ID != fLLM.ID || porPR[1].ID != fSAST.ID {
		t.Errorf("ListFindingsByPR debe traer los hallazgos de ambas corridas ordenados: got %+v", porPR)
	}
}

func TestCommentsSentDedupYTipos(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	repo := f1Repo(t, st)
	pr := f1PR(t, st, repo.ID, 12)
	rev, err := st.CreateReview(ctx, CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h12", BaseSha: "b12"})
	if err != nil {
		t.Fatal(err)
	}

	// Inline con huella de dedup (§3.6).
	inline1, err := st.CreateCommentSent(ctx, CreateCommentSentParams{
		PullRequestID: pr.ID, ReviewID: pgtype.Int8{Int64: rev.ID, Valid: true},
		CommentID: "gh-1", Type: "inline",
		File: f1Text("main.go"), Category: f1Text("security"), Anchor: f1Text("main.go|security|42"),
	})
	if err != nil {
		t.Fatalf("CreateCommentSent inline: %v", err)
	}
	dup, err := st.GetCommentSentByAnchor(ctx, GetCommentSentByAnchorParams{PullRequestID: pr.ID, Anchor: f1Text("main.go|security|42")})
	if err != nil {
		t.Fatalf("GetCommentSentByAnchor: %v", err)
	}
	if dup.ID != inline1.ID {
		t.Errorf("la huella debe resolver el comentario existente: got %d want %d", dup.ID, inline1.ID)
	}
	if _, err := st.CreateCommentSent(ctx, CreateCommentSentParams{
		PullRequestID: pr.ID, ReviewID: pgtype.Int8{Int64: rev.ID, Valid: true},
		CommentID: "gh-2", Type: "inline",
		File: f1Text("a.py"), Category: f1Text("style"), Anchor: f1Text("a.py|style|3"),
	}); err != nil {
		t.Fatalf("CreateCommentSent inline 2: %v", err)
	}
	if _, err := st.GetCommentSentByAnchor(ctx, GetCommentSentByAnchorParams{PullRequestID: pr.ID, Anchor: f1Text("no-existe")}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("una huella nueva no debe resolver nada, got %v", err)
	}

	// Summary y chat (con padre, §9.12): tipos distintos, filas propias.
	if _, err := st.CreateCommentSent(ctx, CreateCommentSentParams{
		PullRequestID: pr.ID, ReviewID: pgtype.Int8{Int64: rev.ID, Valid: true},
		CommentID: "gh-3", Type: "summary",
	}); err != nil {
		t.Fatalf("CreateCommentSent summary: %v", err)
	}
	chat, err := st.CreateCommentSent(ctx, CreateCommentSentParams{
		PullRequestID: pr.ID, ReviewID: pgtype.Int8{Int64: rev.ID, Valid: true},
		CommentID: "gh-4", Type: "chat", ParentCommentID: f1Text("gh-1"),
	})
	if err != nil {
		t.Fatalf("CreateCommentSent chat: %v", err)
	}
	if !chat.ParentCommentID.Valid || chat.ParentCommentID.String != "gh-1" {
		t.Errorf("la respuesta de chat debe anclar a su comentario padre: got %+v", chat)
	}

	inlines, err := st.GetCommentsSentByPRAndType(ctx, GetCommentsSentByPRAndTypeParams{PullRequestID: pr.ID, Type: "inline"})
	if err != nil || len(inlines) != 2 {
		t.Fatalf("GetCommentsSentByPRAndType inline: n=%d err=%v", len(inlines), err)
	}
	summaries, err := st.GetCommentsSentByPRAndType(ctx, GetCommentsSentByPRAndTypeParams{PullRequestID: pr.ID, Type: "summary"})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("GetCommentsSentByPRAndType summary: n=%d err=%v", len(summaries), err)
	}

	// Edición in-place: cambia el ID externo del comentario.
	editado, err := st.UpdateCommentSentCommentID(ctx, UpdateCommentSentCommentIDParams{ID: inline1.ID, CommentID: "gh-1-editado"})
	if err != nil {
		t.Fatalf("UpdateCommentSentCommentID: %v", err)
	}
	if editado.CommentID != "gh-1-editado" {
		t.Errorf("UpdateCommentSentCommentID: got %q", editado.CommentID)
	}
}

func TestWebhookDeliveriesDedup(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// Primera entrega: inserta y devuelve la fila.
	row, err := st.CreateWebhookDelivery(ctx, CreateWebhookDeliveryParams{Vcs: "github", DeliveryID: fmt.Sprintf("d-%d", f1Nano())})
	if err != nil {
		t.Fatalf("primera entrega: %v", err)
	}

	// Reintento/re-entrega del mismo delivery: sin filas → ya procesado.
	if _, err := st.CreateWebhookDelivery(ctx, CreateWebhookDeliveryParams{Vcs: row.Vcs, DeliveryID: row.DeliveryID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("el duplicado debe devolver pgx.ErrNoRows, got %v", err)
	}

	// Los IDs de ambos proveedores no comparten espacio: otro vcs, sí inserta.
	if _, err := st.CreateWebhookDelivery(ctx, CreateWebhookDeliveryParams{Vcs: "gitlab", DeliveryID: row.DeliveryID}); err != nil {
		t.Fatalf("el mismo delivery_id en otro vcs debe insertar: %v", err)
	}

	ok, err := st.WebhookDeliveryExists(ctx, WebhookDeliveryExistsParams{Vcs: row.Vcs, DeliveryID: row.DeliveryID})
	if err != nil || !ok {
		t.Errorf("Exists: got %v err=%v", ok, err)
	}
	ok, err = st.WebhookDeliveryExists(ctx, WebhookDeliveryExistsParams{Vcs: "github", DeliveryID: "nunca-entregado"})
	if err != nil || ok {
		t.Errorf("Exists de un delivery nuevo: got %v err=%v", ok, err)
	}
}

func TestLlmUsage(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	jobID := f1Nano()
	porJob, err := st.CreateLlmUsage(ctx, CreateLlmUsageParams{
		JobID: pgtype.Int8{Int64: jobID, Valid: true},
		Role:  "review", Provider: "openrouter", Model: "modelo-a",
		TokensIn: 10, TokensOut: 5,
	})
	if err != nil {
		t.Fatalf("CreateLlmUsage: %v", err)
	}
	// Prueba de conexión de settings: sin job que la respalde (§3.3).
	if _, err := st.CreateLlmUsage(ctx, CreateLlmUsageParams{
		JobID: pgtype.Int8{}, Role: "cheap", Provider: "openrouter", Model: "modelo-b",
		TokensIn: 20, TokensOut: 10,
	}); err != nil {
		t.Fatalf("CreateLlmUsage sin job: %v", err)
	}

	porJobLista, err := st.ListLlmUsageByJob(ctx, pgtype.Int8{Int64: jobID, Valid: true})
	if err != nil || len(porJobLista) != 1 || porJobLista[0].ID != porJob.ID {
		t.Fatalf("ListLlmUsageByJob: n=%d err=%v", len(porJobLista), err)
	}

	// Ventana futura: nada → ceros; ventana que cubre ahora: al menos lo nuestro.
	futuro, err := st.SumLlmUsageByDateRange(ctx, SumLlmUsageByDateRangeParams{
		CreatedAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		CreatedAt_2: pgtype.Timestamptz{Time: time.Now().Add(2 * time.Hour), Valid: true},
	})
	if err != nil || futuro.TokensIn != 0 || futuro.TokensOut != 0 {
		t.Errorf("ventana sin filas debe sumar 0: got %+v err=%v", futuro, err)
	}
	suma, err := st.SumLlmUsageByDateRange(ctx, SumLlmUsageByDateRangeParams{
		CreatedAt:   pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		CreatedAt_2: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	// La BD de desarrollo es compartida: la suma puede incluir filas de otras
	// corridas — debe cubrir, al menos, las dos de este test.
	if err != nil || suma.TokensIn < 30 || suma.TokensOut < 15 {
		t.Errorf("la suma de la ventana debe cubrir este test: got %+v err=%v", suma, err)
	}
}
