package api

// Tests de trazabilidad de merges sin PR (trazabilidad-merges S1/T4): lista
// con repo + estado de la corrida + conteos por severidad, detalle con
// findings (la MISMA forma que el detalle de PR) y diff vía GetCommitDiff
// del adapter (T2). Member-visible como /api/prs (§3.4: el member opera el
// triage y consulta), solo lectura — auth sin csrf.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// stubVCSCommit graba el (repo, before, after) del último GetCommitDiff: el
// test verifica que el handler despachó el push correcto con la ventana
// correcta. Solo GetCommitDiff se ejercita (embed del stubVCS de prs_test
// por el resto de la interfaz, que el handler de diff jamás toca).
type stubVCSCommit struct {
	stubVCS
	diff string
	err  error

	repo   *store.Repository
	before string
	after  string
}

func (s *stubVCSCommit) GetCommitDiff(_ context.Context, repo *store.Repository, before, after string) (string, error) {
	s.repo, s.before, s.after = repo, before, after
	return s.diff, s.err
}

// pushFixture es un repo + pushes sembrados para los tests de trazabilidad.
type pushFixture struct {
	repo   store.Repository
	pushes map[string]store.DirectPush // por nombre
	revs   map[string]store.Review     // por nombre
	files  []int64                     // hallazgos creados (limpieza)
}

// seedPushes arma: repo github; pushA con 2 corridas (la última success con
// 1 medium) y pushB sin corridas — espejo de seedPRs para la ventana del
// push. La limpieza borra en orden FK (findings → reviews → pushes → repo).
func seedPushes(t *testing.T, e *testEnv) *pushFixture {
	t.Helper()
	ctx := context.Background()
	fx := &pushFixture{pushes: map[string]store.DirectPush{}, revs: map[string]store.Review{}}

	repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "github", ExternalID: time.Now().UnixNano()%1_000_000 + 1, Owner: "acme", Name: "pushes-api-test",
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	fx.repo = repo

	crearPush := func(nombre, before, committer string) store.DirectPush {
		t.Helper()
		p, err := e.st.UpsertDirectPush(ctx, store.UpsertDirectPushParams{
			RepositoryID: repo.ID,
			Branch:       "main",
			BeforeSha:    before,
			AfterSha:     fmt.Sprintf("sha-after-%s-%d", nombre, time.Now().UnixNano()),
			Committer:    committer,
		})
		if err != nil {
			t.Fatalf("UpsertDirectPush(%s): %v", nombre, err)
		}
		fx.pushes[nombre] = p
		return p
	}

	// pushB primero: su created_at queda anterior al de pushA (orden DESC
	// del listado; id DESC desempata si el microsegundo coincide).
	crearPush("b", "sha-base-b", "dev-b")
	pushA := crearPush("a", "sha-base-a", "dev-a")

	// Corrida vieja de pushA: queda stale (distingue la latest de cualquiera).
	r1, err := e.st.CreateReview(ctx, store.CreateReviewParams{
		DirectPushID: pgtype.Int8{Int64: pushA.ID, Valid: true},
		HeadSha:      pushA.AfterSha,
		BaseSha:      pushA.BeforeSha,
	})
	if err != nil {
		t.Fatalf("CreateReview r1: %v", err)
	}
	if _, err := e.st.UpdateReviewStatus(ctx, store.UpdateReviewStatusParams{ID: r1.ID, Status: "stale"}); err != nil {
		t.Fatalf("UpdateReviewStatus r1: %v", err)
	}
	// Corrida más reciente (id mayor): la "latest" del listado y del detalle.
	r2, err := e.st.CreateReview(ctx, store.CreateReviewParams{
		DirectPushID: pgtype.Int8{Int64: pushA.ID, Valid: true},
		HeadSha:      pushA.AfterSha,
		BaseSha:      pushA.BeforeSha,
	})
	if err != nil {
		t.Fatalf("CreateReview r2: %v", err)
	}
	fx.revs["latest"] = r2

	if _, err := e.st.UpdateReviewSummary(ctx, store.UpdateReviewSummaryParams{
		ID: r2.ID, Summary: "resumen-push", Walkthrough: "paseo-push", Mermaid: "graph TD; P-->R",
	}); err != nil {
		t.Fatalf("UpdateReviewSummary: %v", err)
	}
	if _, err := e.st.UpdateReviewStatus(ctx, store.UpdateReviewStatusParams{ID: r2.ID, Status: "success"}); err != nil {
		t.Fatalf("UpdateReviewStatus r2: %v", err)
	}

	crearFinding := func(reviewID int64, file string, line int32, sev, cat, body string, sug pgtype.Text, source string, ver pgtype.Bool) {
		t.Helper()
		f, err := e.st.CreateFinding(ctx, store.CreateFindingParams{
			ReviewID: reviewID, File: file, Line: line, Severity: sev, Category: cat,
			Body: body, Suggestion: sug, Source: source, Verified: ver,
		})
		if err != nil {
			t.Fatalf("CreateFinding(%s:%d): %v", file, line, err)
		}
		fx.files = append(fx.files, f.ID)
	}
	// Corrida vieja (r1): high + low — el detalle las ordena por severidad y
	// file/line y el conteo del listado las IGNORA (mira solo la latest).
	crearFinding(r1.ID, "b.go", 10, "high", "security", "sql injection", pgtype.Text{}, "llm", pgtype.Bool{})
	crearFinding(r1.ID, "a.go", 5, "low", "style", "nombre corto", pgtype.Text{}, "sast", pgtype.Bool{Bool: false, Valid: true})
	// Corrida latest (r2): un medium con sugerencia y verificación.
	crearFinding(r2.ID, "c.go", 3, "medium", "performance", "concat en loop",
		pgtype.Text{String: "usar strings.Builder", Valid: true}, "llm", pgtype.Bool{Bool: true, Valid: true})

	t.Cleanup(func() {
		// Sin ON DELETE en cascada (§3.4): borrar hijos antes que el padre
		// (precedente seedPRs).
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM findings WHERE id = ANY($1)", fx.files)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM reviews WHERE direct_push_id = ANY($1)",
			[]int64{fx.pushes["a"].ID, fx.pushes["b"].ID})
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM direct_pushes WHERE repository_id = $1", repo.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repo.ID)
	})
	return fx
}

// Sin sesión → 401; con sesión de member → 200 (§3.4: el member consulta la
// trazabilidad igual que los PRs).
func TestPushesAuthYMemberVisible(t *testing.T) {
	e := newTestEnv(t, 5)
	m := e.member(t)

	if got := e.do(t, http.MethodGet, "/api/pushes", nil, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("GET /api/pushes sin sesión debe ser 401, fue %d", got)
	}
	if got := e.do(t, http.MethodGet, "/api/pushes/1", nil, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("GET /api/pushes/{id} sin sesión debe ser 401, fue %d", got)
	}
	if got := e.do(t, http.MethodGet, "/api/pushes", m.cookie, m.csrf, nil).StatusCode; got != http.StatusOK {
		t.Errorf("GET /api/pushes como member debe ser 200, fue %d", got)
	}
}

// El listado trae los pushes con repo (vcs incluido) y el estado de su
// corrida más reciente — UNA fila por push (el JOIN matchea solo la latest,
// no una por corrida) con los conteos por severidad de ESA corrida; sin
// corrida → latest_review null y conteos en cero. Orden: created_at DESC
// (pushA se sembró después de pushB).
func TestPushesListadoConUltimaReviewYConteos(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedPushes(t, e)
	a := e.admin(t)

	resp := e.do(t, http.MethodGet, "/api/pushes", a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/pushes debe ser 200, fue %d", resp.StatusCode)
	}
	var list []pushView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}

	buscar := func(id int64) *pushView {
		for i := range list {
			if list[i].ID == id {
				return &list[i]
			}
		}
		return nil
	}
	// UNA fila por push: pushA tiene 2 corridas y debe aparecer una sola vez.
	veces, vA := 0, (*pushView)(nil)
	for i := range list {
		if list[i].ID == fx.pushes["a"].ID {
			veces++
			vA = &list[i]
		}
	}
	if vA == nil || veces != 1 {
		t.Fatalf("pushA debe aparecer EXACTAMENTE una vez (apareció %d): %+v", veces, list)
	}
	// Shas COMPLETOS: son la identidad del merge, no recorte de display.
	if vA.Branch != "main" || vA.BeforeSha != fx.pushes["a"].BeforeSha ||
		vA.AfterSha != fx.pushes["a"].AfterSha || vA.Committer != "dev-a" {
		t.Errorf("pushA con sus datos (shas completos): %+v", vA)
	}
	if vA.Repo.ID != fx.repo.ID || vA.Repo.Owner != "acme" || vA.Repo.Name != "pushes-api-test" || vA.Repo.Vcs != "github" {
		t.Errorf("pushA debe embeber el repo (vcs incluido): %+v", vA.Repo)
	}
	if vA.LatestReview == nil {
		t.Fatal("pushA debe traer latest_review")
	}
	if vA.LatestReview.ID != fx.revs["latest"].ID || vA.LatestReview.Status != "success" {
		t.Errorf("latest_review debe ser la corrida más reciente: %+v", vA.LatestReview)
	}
	// Conteos de la corrida LATEST solamente: 1 medium, no los de la vieja.
	if got := vA.FindingsCount; got != (reviewCounts{Medium: 1}) {
		t.Errorf("findings_count de la última corrida deben ser {0,1,0}, fueron %+v", got)
	}

	vB := buscar(fx.pushes["b"].ID)
	if vB == nil {
		t.Fatalf("pushB debe aparecer en el listado: %+v", list)
	}
	if vB.LatestReview != nil {
		t.Errorf("pushB sin corridas debe traer latest_review null: %+v", vB.LatestReview)
	}
	if vB.FindingsCount != (reviewCounts{}) {
		t.Errorf("pushB sin corridas: findings_count en cero, fue %+v", vB.FindingsCount)
	}

	// Orden created_at DESC: pushA (sembrado después) primero.
	var orden []int64
	for _, v := range list {
		if v.ID == fx.pushes["a"].ID || v.ID == fx.pushes["b"].ID {
			orden = append(orden, v.ID)
		}
	}
	if len(orden) != 2 || orden[0] != fx.pushes["a"].ID || orden[1] != fx.pushes["b"].ID {
		t.Errorf("el listado debe ir created_at DESC (a antes que b): %v", orden)
	}
}

// El detalle junta push + última corrida (textos completados) + TODOS los
// findings de todas las corridas — la MISMA forma y orden que el detalle de
// PR (severity → file/line, verified/suggestion nullables reales). El
// dismissed llega por la API de dismiss (S4): la huella (repo, file,
// category) es del REPO, así que un dismiss sobre un finding de PR con la
// misma huella marca los hallazgos del push igual (mismo mecanismo que
// entre corridas de un PR).
func TestPushDetalleConFindingsYDismissed(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedPushes(t, e)
	a := e.admin(t)
	ctx := context.Background()

	leer := func() pushDetailView {
		t.Helper()
		resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/pushes/%d", fx.pushes["a"].ID), a.cookie, a.csrf, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET detalle debe ser 200, fue %d", resp.StatusCode)
		}
		var out pushDetailView
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	out := leer()
	if out.Push.ID != fx.pushes["a"].ID || out.Push.Branch != "main" ||
		out.Push.BeforeSha != fx.pushes["a"].BeforeSha || out.Push.AfterSha != fx.pushes["a"].AfterSha ||
		out.Push.Committer != "dev-a" {
		t.Errorf("el bloque push debe traer el push completo (shas completos): %+v", out.Push)
	}
	if out.Push.Repo.ID != fx.repo.ID || out.Push.Repo.Owner != "acme" ||
		out.Push.Repo.Name != "pushes-api-test" || out.Push.Repo.Vcs != "github" {
		t.Errorf("el bloque push debe embeber el repo: %+v", out.Push.Repo)
	}
	if out.Review == nil {
		t.Fatal("pushA tiene corridas: review no debe ser null")
	}
	if out.Review.ID != fx.revs["latest"].ID || out.Review.Status != "success" ||
		out.Review.Summary != "resumen-push" || out.Review.Walkthrough != "paseo-push" ||
		out.Review.Mermaid != "graph TD; P-->R" {
		t.Errorf("review debe ser la corrida latest con sus textos: %+v", out.Review)
	}

	// Orden esperado (misma regla que el detalle de PR): high(b.go:10) →
	// medium(c.go:3, con sugerencia y verificación) → low(a.go:5).
	queres := []struct {
		file     string
		line     int32
		severity string
		verified *bool
		sug      *string
	}{
		{file: "b.go", line: 10, severity: "high", verified: nil, sug: nil},
		{file: "c.go", line: 3, severity: "medium", verified: boolPtr(true), sug: strPtr("usar strings.Builder")},
		{file: "a.go", line: 5, severity: "low", verified: boolPtr(false), sug: nil},
	}
	if len(out.Findings) != len(queres) {
		t.Fatalf("deben listarse los findings de TODAS las corridas (%d), fueron %d", len(queres), len(out.Findings))
	}
	for i, q := range queres {
		f := out.Findings[i]
		if f.File != q.file || f.Line != q.line || f.Severity != q.severity {
			t.Errorf("finding %d: esperaba %s:%d (%s), fue %s:%d (%s)", i, q.file, q.line, q.severity, f.File, f.Line, f.Severity)
		}
		// La atribución por corrida viaja en el finding (§3.3).
		if f.ReviewID <= 0 {
			t.Errorf("finding %d (%s): review_id debe traer la corrida que lo produjo, fue %d", i, q.file, f.ReviewID)
		}
		if f.Dismissed {
			t.Errorf("finding %d (%s): sin dismiss previo no llega marcado", i, q.file)
		}
		if (f.Verified == nil) != (q.verified == nil) || (f.Verified != nil && *f.Verified != *q.verified) {
			t.Errorf("finding %d (%s): verified esperaba %v, fue %v", i, q.file, q.verified, f.Verified)
		}
		if (f.Suggestion == nil) != (q.sug == nil) || (f.Suggestion != nil && *f.Suggestion != *q.sug) {
			t.Errorf("finding %d (%s): suggestion esperaba %v, fue %v", i, q.file, q.sug, f.Suggestion)
		}
		if f.Body == "" || f.Category == "" || f.Source == "" {
			t.Errorf("finding %d debe traer body/category/source: %+v", i, f)
		}
	}

	// Dismiss via API (S4): un PR del MISMO repo con un finding de la misma
	// huella (b.go, security) produce el dismiss por la interfaz pública.
	pr, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: fx.repo.ID, Number: time.Now().UnixNano()%100_000 + 1, Author: "dev-pr",
		State: "open", HeadSha: "sha-pr-head", BaseRef: "main", BaseSha: "sha-pr-base",
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}
	prRev, err := e.st.CreateReview(ctx, store.CreateReviewParams{
		PullRequestID: pgtype.Int8{Int64: pr.ID, Valid: true}, HeadSha: "sha-pr-head", BaseSha: "sha-pr-base",
	})
	if err != nil {
		t.Fatalf("CreateReview (PR): %v", err)
	}
	prF, err := e.st.CreateFinding(ctx, store.CreateFindingParams{
		ReviewID: prRev.ID, File: "b.go", Line: 42, Severity: "low", Category: "security",
		Body: "mismo hueco en el PR", Source: "sast",
	})
	if err != nil {
		t.Fatalf("CreateFinding (PR): %v", err)
	}
	t.Cleanup(func() {
		// LIFO: corre antes que el cleanup de seedPushes (borra el repo).
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM finding_dismissals WHERE repository_id=$1", fx.repo.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM findings WHERE review_id=$1", prRev.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM reviews WHERE id=$1", prRev.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM pull_requests WHERE id=$1", pr.ID)
	})

	if resp := e.do(t, http.MethodPost, fmt.Sprintf("/api/findings/%d/dismiss", prF.ID), a.cookie, a.csrf,
		map[string]string{"reason": "falso positivo"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss via API = %d, querés 200", resp.StatusCode)
	}

	out = leer()
	for _, f := range out.Findings {
		want := f.File == "b.go" && f.Category == "security"
		if f.Dismissed != want {
			t.Errorf("%s:%d (%s): dismissed=%v, querés %v (huella b.go/security del repo)",
				f.File, f.Line, f.Category, f.Dismissed, want)
		}
	}

	// {id} inexistente → 404; el member también consulta el detalle (§3.4).
	if got := e.do(t, http.MethodGet, "/api/pushes/99999999", a.cookie, a.csrf, nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("detalle de push inexistente debe ser 404, fue %d", got)
	}
	m := e.member(t)
	if got := e.do(t, http.MethodGet, fmt.Sprintf("/api/pushes/%d", fx.pushes["a"].ID), m.cookie, m.csrf, nil).StatusCode; got != http.StatusOK {
		t.Errorf("detalle como member debe ser 200, fue %d", got)
	}
}

// El diff del push va al adapter con la ventana (before, after) del push: el
// stub graba lo que recibió; el error del VCS es 502 (upstream); id
// inexistente es 404. Espejo de TestPRDiffDespachaAlAdapter.
func TestPushDiffDespachaAlAdapter(t *testing.T) {
	gh := &stubVCSCommit{diff: "diff --git a/x.go b/x.go\n+merge directo\n"}
	e := newTestEnvVCS(t, 5, gh, nil)
	fx := seedPushes(t, e)
	a := e.admin(t)

	resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/pushes/%d/diff", fx.pushes["a"].ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET diff debe ser 200, fue %d", resp.StatusCode)
	}
	var out struct {
		Diff string `json:"diff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Diff != gh.diff {
		t.Errorf("el cuerpo debe ser el diff del stub: %q", out.Diff)
	}
	// El handler despachó el repo y la ventana correctos al adapter.
	if gh.repo == nil || gh.repo.ID != fx.repo.ID {
		t.Errorf("GetCommitDiff debe recibir el repo (%d) correcto: %+v", fx.repo.ID, gh.repo)
	}
	if gh.before != fx.pushes["a"].BeforeSha || gh.after != fx.pushes["a"].AfterSha {
		t.Errorf("GetCommitDiff debe recibir (before, after) del push: got (%q, %q), querés (%q, %q)",
			gh.before, gh.after, fx.pushes["a"].BeforeSha, fx.pushes["a"].AfterSha)
	}

	// Upstream caído → 502, no 500 ni 404.
	ghFalla := &stubVCSCommit{err: errors.New("vcs inalcanzable")}
	e2 := newTestEnvVCS(t, 5, ghFalla, nil)
	fx2 := seedPushes(t, e2)
	a2 := e2.admin(t)
	if got := e2.do(t, http.MethodGet, fmt.Sprintf("/api/pushes/%d/diff", fx2.pushes["a"].ID), a2.cookie, a2.csrf, nil).StatusCode; got != http.StatusBadGateway {
		t.Errorf("GetCommitDiff con error debe ser 502, fue %d", got)
	}

	// Push inexistente → 404.
	if got := e.do(t, http.MethodGet, "/api/pushes/99999999/diff", a.cookie, a.csrf, nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("diff de push inexistente debe ser 404, fue %d", got)
	}
}

// El dispatch respeta repo.vcs (espejo de TestPRDiffEligeAdapterPorRepo):
// un push de repo gitlab va al adapter gitlab aunque ambos estén montados.
func TestPushDiffEligeAdapterPorRepo(t *testing.T) {
	gh := &stubVCSCommit{diff: "no-debe-salir"}
	gl := &stubVCSCommit{diff: "diff --git a/y.go b/y.go\n"}
	e := newTestEnvVCS(t, 5, gh, gl)
	ctx := context.Background()

	repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "gitlab", ExternalID: time.Now().UnixNano()%1_000_000 + 1, Owner: "acme", Name: "pushes-gitlab-test",
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	push, err := e.st.UpsertDirectPush(ctx, store.UpsertDirectPushParams{
		RepositoryID: repo.ID, Branch: "main", BeforeSha: "gl-base", AfterSha: "gl-head", Committer: "dev-gl",
	})
	if err != nil {
		t.Fatalf("UpsertDirectPush: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM direct_pushes WHERE repository_id = $1", repo.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repo.ID)
	})

	a := e.admin(t)
	var out struct {
		Diff string `json:"diff"`
	}
	resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/pushes/%d/diff", push.ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET diff gitlab debe ser 200, fue %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Diff != gl.diff {
		t.Errorf("repo gitlab debe ir al adapter gitlab: %q", out.Diff)
	}
	if gh.repo != nil {
		t.Error("el adapter github no debe recibir el push de un repo gitlab")
	}
}

// El dismiss es polimórfico (trazabilidad T4): un hallazgo de una corrida de
// PUSH directo se puede marcar "no aplica" — la huella (repo, file, category)
// cruza targets. Antes del fix, el handler revolvía el repo vía PR y daba 500.
func TestDismissFindingDePush(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedPushes(t, e)
	a := e.admin(t)

	// files[2] es el medium (c.go, performance) de la corrida LATEST del push A.
	resp := e.do(t, http.MethodPost, fmt.Sprintf("/api/findings/%d/dismiss", fx.files[2]), a.cookie, a.csrf,
		map[string]string{"reason": "falso positivo del push"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dismiss de finding de push = %d, querés 200", resp.StatusCode)
	}

	// El detalle del push lo trae marcado (huella viva).
	resp = e.do(t, http.MethodGet, fmt.Sprintf("/api/pushes/%d", fx.pushes["a"].ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET detalle push = %d, querés 200", resp.StatusCode)
	}
	var out pushDetailView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	marcados := 0
	for _, f := range out.Findings {
		if f.File == "c.go" && !f.Dismissed {
			t.Error("c.go debe llegar marcado: la huella vive en el repo")
		}
		if f.Dismissed {
			marcados++
		}
	}
	if marcados != 1 {
		t.Errorf("hallazgos marcados = %d, querés 1 (solo c.go)", marcados)
	}
}
