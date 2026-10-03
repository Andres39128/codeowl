package api

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
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// Tests del detalle de PR (guía §6 F3): lista con última corrida + conteos,
// detalle con findings y diff vía GetDiff del adapter. Member-visible (§3.4:
// el member opera el triage y consulta PRs), solo lectura — auth sin csrf.

// stubVCS graba el (repo, pr) del último GetDiff: el test verifica que el
// handler despachó el PR correcto al adapter correcto. Solo GetDiff se
// ejercita acá — el resto de la interfaz queda sin usar (el handler de diff
// jamás la toca).
type stubVCS struct {
	vcs.VCSProvider
	diff string
	err  error

	repo *store.Repository
	pr   *store.PullRequest
}

func (s *stubVCS) GetDiff(_ context.Context, repo *store.Repository, pr *store.PullRequest) (string, error) {
	s.repo, s.pr = repo, pr
	return s.diff, s.err
}

// prFixture es un repo + PRs sembrados para los tests de lectura.
type prFixture struct {
	repo  store.Repository
	prs   map[string]store.PullRequest // por nombre
	revs  map[string]store.Review      // por nombre
	files []int64                      // hallazgos creados (limpieza)
}

// seedPRs arma: repo github; prA con 2 corridas (la última con 1 medium);
// prB sin corridas. Devuelve los ids para los asserts. La limpieza borra en
// orden FK (findings → reviews → PRs → repo): no hay ON DELETE en cascada.
func seedPRs(t *testing.T, e *testEnv) *prFixture {
	t.Helper()
	ctx := context.Background()
	fx := &prFixture{prs: map[string]store.PullRequest{}, revs: map[string]store.Review{}}

	repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "github", ExternalID: time.Now().UnixNano()%1_000_000 + 1, Owner: "acme", Name: "prs-api-test",
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	fx.repo = repo

	crearPR := func(nombre string) store.PullRequest {
		t.Helper()
		pr, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
			RepositoryID: repo.ID,
			Number:       time.Now().UnixNano()%100_000 + 1,
			Author:       "dev-" + nombre,
			State:        "open",
			HeadSha:      fmt.Sprintf("sha-head-%s", nombre),
			BaseRef:      "main",
			BaseSha:      "sha-base",
			CreatedAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		if err != nil {
			t.Fatalf("UpsertPullRequest(%s): %v", nombre, err)
		}
		fx.prs[nombre] = pr
		return pr
	}

	// prB primero: su updated_at queda anterior al de prA (orden del listado).
	prB := crearPR("b")
	prA := crearPR("a")

	// Corrida vieja de prA: high + dos low (verifican orden del detalle).
	r1, err := e.st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: prA.ID, HeadSha: prA.HeadSha, BaseSha: prA.BaseSha})
	if err != nil {
		t.Fatalf("CreateReview r1: %v", err)
	}
	// Corrida más reciente (id mayor): la "latest" del listado y del detalle.
	r2, err := e.st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: prA.ID, HeadSha: prA.HeadSha, BaseSha: prA.BaseSha})
	if err != nil {
		t.Fatalf("CreateReview r2: %v", err)
	}
	fx.revs["latest"] = r2

	if _, err := e.st.UpdateReviewSummary(ctx, store.UpdateReviewSummaryParams{
		ID: r2.ID, Summary: "resumen-final", Walkthrough: "paseo-final", Mermaid: "graph TD; A-->B",
	}); err != nil {
		t.Fatalf("UpdateReviewSummary: %v", err)
	}
	if _, err := e.st.UpdateReviewStatus(ctx, store.UpdateReviewStatusParams{ID: r2.ID, Status: "success"}); err != nil {
		t.Fatalf("UpdateReviewStatus: %v", err)
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
	// Corrida vieja (r1): high + low + low — el detalle las ordena por
	// severidad y file/line, sin importar la corrida que las produjo.
	crearFinding(r1.ID, "b.go", 10, "high", "security", "sql injection", pgtype.Text{}, "llm", pgtype.Bool{})
	crearFinding(r1.ID, "a.go", 5, "low", "style", "nombre corto", pgtype.Text{}, "sast", pgtype.Bool{Bool: false, Valid: true})
	crearFinding(r1.ID, "a.go", 1, "low", "style", "otro estilo", pgtype.Text{}, "sast", pgtype.Bool{})
	// Corrida latest (r2): un medium con sugerencia y verificación.
	crearFinding(r2.ID, "c.go", 3, "medium", "performance", "concat en loop",
		pgtype.Text{String: "usar strings.Builder", Valid: true}, "llm", pgtype.Bool{Bool: true, Valid: true})

	t.Cleanup(func() {
		// Sin ON DELETE en cascada (§3.4): borrar hijos antes que el padre.
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM findings WHERE id = ANY($1)", fx.files)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM reviews WHERE pull_request_id IN ($1, $2)", prA.ID, prB.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM pull_requests WHERE repository_id = $1", repo.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repo.ID)
	})
	return fx
}

// Sin sesión → 401; con sesión de member → 200 (§3.4: el member consulta PRs).
func TestPRsAuthYMemberVisible(t *testing.T) {
	e := newTestEnv(t, 5)
	m := e.member(t)

	if got := e.do(t, http.MethodGet, "/api/prs", nil, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("GET /api/prs sin sesión debe ser 401, fue %d", got)
	}
	if got := e.do(t, http.MethodGet, "/api/prs", m.cookie, m.csrf, nil).StatusCode; got != http.StatusOK {
		t.Errorf("GET /api/prs como member debe ser 200, fue %d", got)
	}
}

// El listado trae TODOS los PRs (los cerrados tienen valor de auditoría) con
// la última corrida y sus conteos; sin corrida → latest_review null. Orden:
// updated_at DESC (prA se sembró después de prB).
func TestPRsListadoConUltimaReviewYConteos(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedPRs(t, e)
	a := e.admin(t)

	resp := e.do(t, http.MethodGet, "/api/prs", a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/prs debe ser 200, fue %d", resp.StatusCode)
	}
	var list []prView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}

	buscar := func(id int64) *prView {
		for i := range list {
			if list[i].ID == id {
				return &list[i]
			}
		}
		return nil
	}
	vA := buscar(fx.prs["a"].ID)
	if vA == nil {
		t.Fatalf("prA debe aparecer en el listado: %+v", list)
	}
	if vA.Author != "dev-a" || vA.State != "open" || vA.HeadSha != fx.prs["a"].HeadSha || vA.BaseRef != "main" {
		t.Errorf("prA con sus datos: %+v", vA)
	}
	if vA.Repo.ID != fx.repo.ID || vA.Repo.Owner != "acme" || vA.Repo.Name != "prs-api-test" || vA.Repo.Vcs != "github" {
		t.Errorf("prA debe embeber el repo: %+v", vA.Repo)
	}
	if vA.LatestReview == nil {
		t.Fatal("prA debe traer latest_review")
	}
	if vA.LatestReview.ID != fx.revs["latest"].ID || vA.LatestReview.Status != "success" {
		t.Errorf("latest_review debe ser la corrida más reciente: %+v", vA.LatestReview)
	}
	// Conteos de la corrida LATEST solamente: 1 medium, no los de la vieja.
	if got := vA.LatestReview.Counts; got != (reviewCounts{Medium: 1}) {
		t.Errorf("conteos de la última corrida deben ser {0,1,0}, fueron %+v", got)
	}

	vB := buscar(fx.prs["b"].ID)
	if vB == nil {
		t.Fatalf("prB debe aparecer en el listado: %+v", list)
	}
	if vB.LatestReview != nil {
		t.Errorf("prB sin corridas debe traer latest_review null: %+v", vB.LatestReview)
	}

	// Orden updated_at DESC: prA (sembrado después) primero.
	var orden []int64
	for _, v := range list {
		if v.ID == fx.prs["a"].ID || v.ID == fx.prs["b"].ID {
			orden = append(orden, v.ID)
		}
	}
	if len(orden) != 2 || orden[0] != fx.prs["a"].ID || orden[1] != fx.prs["b"].ID {
		t.Errorf("el listado debe ir updated_at DESC (a antes que b): %v", orden)
	}
}

// El detalle junta PR + última corrida (textos completados) + TODOS los
// findings de todas las corridas, ordenados high → low y por file/line.
// verified y suggestion son nullables reales (§3.3: verified null hasta que
// corre el Verifier).
func TestPRDetalleConFindingsOrdenados(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedPRs(t, e)
	a := e.admin(t)

	resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/prs/%d", fx.prs["a"].ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET detalle debe ser 200, fue %d", resp.StatusCode)
	}
	var out prDetailView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	if out.PR.ID != fx.prs["a"].ID || out.PR.Repo.ID != fx.repo.ID {
		t.Errorf("el bloque pr debe traer PR y repo: %+v", out.PR)
	}
	if out.Review == nil {
		t.Fatal("prA tiene corridas: review no debe ser null")
	}
	if out.Review.ID != fx.revs["latest"].ID || out.Review.Status != "success" ||
		out.Review.Summary != "resumen-final" || out.Review.Walkthrough != "paseo-final" ||
		out.Review.Mermaid != "graph TD; A-->B" {
		t.Errorf("review debe ser la corrida latest con sus textos: %+v", out.Review)
	}

	// Orden esperado: high(b.go:10) → medium(c.go:3) → low(a.go:1) → low(a.go:5).
	queres := []struct {
		file     string
		line     int32
		severity string
		verified *bool
		sug      *string
	}{
		{file: "b.go", line: 10, severity: "high", verified: nil, sug: nil},
		{file: "c.go", line: 3, severity: "medium", verified: boolPtr(true), sug: strPtr("usar strings.Builder")},
		{file: "a.go", line: 1, severity: "low", verified: nil, sug: nil},
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

	// {id} inexistente → 404.
	if got := e.do(t, http.MethodGet, "/api/prs/99999999", a.cookie, a.csrf, nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("detalle de PR inexistente debe ser 404, fue %d", got)
	}
	// El member también consulta el detalle (§3.4).
	m := e.member(t)
	if got := e.do(t, http.MethodGet, fmt.Sprintf("/api/prs/%d", fx.prs["a"].ID), m.cookie, m.csrf, nil).StatusCode; got != http.StatusOK {
		t.Errorf("detalle como member debe ser 200, fue %d", got)
	}
}

// El diff va al adapter que repo.vcs indique: el stub github recibe el PR y
// repo correctos; el error del VCS es 502 (upstream); id inexistente es 404.
func TestPRDiffDespachaAlAdapter(t *testing.T) {
	gh := &stubVCS{diff: "diff --git a/x.go b/x.go\n+una linea\n"}
	e := newTestEnvVCS(t, 5, gh, nil)
	fx := seedPRs(t, e)
	a := e.admin(t)

	resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/prs/%d/diff", fx.prs["a"].ID), a.cookie, a.csrf, nil)
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
	// El handler despachó el PR y repo correctos al adapter.
	if gh.pr == nil || gh.pr.ID != fx.prs["a"].ID || gh.repo.ID != fx.repo.ID {
		t.Errorf("GetDiff debe recibir el PR (%d) y repo (%d) correctos: pr=%+v repo=%+v",
			fx.prs["a"].ID, fx.repo.ID, gh.pr, gh.repo)
	}

	// Upstream caído → 502, no 500 ni 404.
	ghFalla := &stubVCS{err: errors.New("vcs inalcanzable")}
	e2 := newTestEnvVCS(t, 5, ghFalla, nil)
	fx2 := seedPRs(t, e2)
	a2 := e2.admin(t)
	if got := e2.do(t, http.MethodGet, fmt.Sprintf("/api/prs/%d/diff", fx2.prs["a"].ID), a2.cookie, a2.csrf, nil).StatusCode; got != http.StatusBadGateway {
		t.Errorf("GetDiff con error debe ser 502, fue %d", got)
	}

	// PR inexistente → 404.
	if got := e.do(t, http.MethodGet, "/api/prs/99999999/diff", a.cookie, a.csrf, nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("diff de PR inexistente debe ser 404, fue %d", got)
	}
}

// El dispatch respeta repo.vcs: un repo gitlab va al adapter gitlab, no al
// github, aunque ambos estén montados.
func TestPRDiffEligeAdapterPorRepo(t *testing.T) {
	gh := &stubVCS{diff: "no-debe-salir"}
	gl := &stubVCS{diff: "diff --git a/y.go b/y.go\n"}
	e := newTestEnvVCS(t, 5, gh, gl)
	ctx := context.Background()

	repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "gitlab", ExternalID: time.Now().UnixNano()%1_000_000 + 1, Owner: "acme", Name: "prs-gitlab-test",
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	pr, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 7, Author: "dev-gl", State: "open",
		HeadSha: "sha-gl", BaseRef: "main", BaseSha: "base-gl",
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM pull_requests WHERE repository_id = $1", repo.ID)
		_, _ = e.st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repo.ID)
	})

	a := e.admin(t)
	var out struct {
		Diff string `json:"diff"`
	}
	resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/prs/%d/diff", pr.ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET diff gitlab debe ser 200, fue %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Diff != gl.diff {
		t.Errorf("repo gitlab debe ir al adapter gitlab: %q", out.Diff)
	}
	if gh.pr != nil {
		t.Error("el adapter github no debe recibir el PR de un repo gitlab")
	}
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

// triageFx es el tablero determinista de los query params (T7, decisión 8):
// repo principal con cinco PRs sembrados en orden (el updated_at crece con
// cada insert, precedente de seedPRs) y un repo secundario con uno propio.
//
//	repo1: slow (open,  score 50, 1°) → fast (open, score 50, 2°) →
//	       top  (open,  score 90, 3°, última corrida 1 high) →
//	       none (open,  score nil, 4°, sin corridas) →
//	       cerrado (closed, score 10, 5°, sin corridas)
//	repo2: otro (open, sin score)
type triageFx struct {
	repo  store.Repository
	repo2 store.Repository
	prs   map[string]store.PullRequest
}

func seedTriage(t *testing.T, e *testEnv) *triageFx {
	t.Helper()
	ctx := context.Background()
	fx := &triageFx{prs: map[string]store.PullRequest{}}

	crearRepo := func(nombre string) store.Repository {
		t.Helper()
		repo, err := e.st.CreateRepository(ctx, store.CreateRepositoryParams{
			Vcs: "github", ExternalID: time.Now().UnixNano()%1_000_000 + 1,
			Owner: "acme", Name: nombre,
		})
		if err != nil {
			t.Fatalf("CreateRepository(%s): %v", nombre, err)
		}
		return repo
	}
	fx.repo = crearRepo("triage-a")
	fx.repo2 = crearRepo("triage-b")

	crearPR := func(repoID int64, nombre, state string) store.PullRequest {
		t.Helper()
		pr, err := e.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
			RepositoryID: repoID,
			Number:       time.Now().UnixNano()%100_000 + 1,
			Author:       "dev-" + nombre,
			State:        state,
			HeadSha:      "sha-" + nombre,
			BaseRef:      "main",
			BaseSha:      "base-" + nombre,
			CreatedAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		if err != nil {
			t.Fatalf("UpsertPullRequest(%s): %v", nombre, err)
		}
		fx.prs[nombre] = pr
		return pr
	}

	// Orden de siembra = orden de updated_at (de más viejo a más nuevo).
	crearPR(fx.repo.ID, "slow", "open")
	crearPR(fx.repo.ID, "fast", "open")
	top := crearPR(fx.repo.ID, "top", "open")
	crearPR(fx.repo.ID, "none", "open")
	crearPR(fx.repo.ID, "cerrado", "closed")
	crearPR(fx.repo2.ID, "otro", "open")

	score := func(nombre string, v int32) {
		t.Helper()
		if err := e.st.UpdatePullRequestRiskScore(ctx, store.UpdatePullRequestRiskScoreParams{
			ID: fx.prs[nombre].ID, RiskScore: pgtype.Int4{Int32: v, Valid: true},
		}); err != nil {
			t.Fatalf("UpdatePullRequestRiskScore(%s): %v", nombre, err)
		}
	}
	score("slow", 50)
	score("fast", 50)
	score("top", 90)
	score("cerrado", 10)

	// top: última corrida con 1 high; fast: 1 low — alimenta severity.
	corrida := func(nombre, sev string) {
		t.Helper()
		pr := fx.prs[nombre]
		rev, err := e.st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: pr.ID, HeadSha: pr.HeadSha, BaseSha: pr.BaseSha})
		if err != nil {
			t.Fatalf("CreateReview(%s): %v", nombre, err)
		}
		if _, err := e.st.CreateFinding(ctx, store.CreateFindingParams{
			ReviewID: rev.ID, File: nombre + ".go", Line: 1, Severity: sev,
			Category: "security", Body: "hallazgo-" + nombre, Source: "llm",
		}); err != nil {
			t.Fatalf("CreateFinding(%s): %v", nombre, err)
		}
	}
	corrida("top", "high")
	corrida("fast", "low")

	t.Cleanup(func() {
		// Hojas primero (precedente seedPRs): findings → reviews → PRs → repos.
		if _, err := e.st.Pool.Exec(ctx,
			"DELETE FROM findings WHERE review_id IN (SELECT id FROM reviews WHERE pull_request_id = ANY($1))",
			[]int64{top.ID, fx.prs["fast"].ID}); err != nil {
			t.Errorf("limpiando findings: %v", err)
		}
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM reviews WHERE pull_request_id = ANY($1)",
			[]int64{top.ID, fx.prs["fast"].ID}); err != nil {
			t.Errorf("limpiando reviews: %v", err)
		}
		for _, repo := range []store.Repository{fx.repo, fx.repo2} {
			if _, err := e.st.Pool.Exec(ctx, "DELETE FROM pull_requests WHERE repository_id = $1", repo.ID); err != nil {
				t.Errorf("limpiando PRs: %v", err)
			}
			if _, err := e.st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repo.ID); err != nil {
				t.Errorf("limpiando repos: %v", err)
			}
		}
	})
	return fx
}

// ids extrae, en el orden del JSON, los ids de los PRs sembrados.
func ids(list []prView, fx *triageFx) []int64 {
	nombres := map[int64]string{}
	for nombre, pr := range fx.prs {
		nombres[pr.ID] = nombre
	}
	out := make([]int64, 0, len(list))
	for _, v := range list {
		if _, ok := nombres[v.ID]; ok {
			out = append(out, v.ID)
		}
	}
	return out
}

func mismos(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// listarPRs pide /api/prs con query y devuelve las vistas decodificadas.
func listarPRs(t *testing.T, e *testEnv, query string) []prView {
	t.Helper()
	a := e.admin(t)
	resp := e.do(t, http.MethodGet, "/api/prs"+query, a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/prs%s debe ser 200, fue %d", query, resp.StatusCode)
	}
	var list []prView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	return list
}

// risk_score viaja en el JSON (90/nil/10) y el detalle lo espeja: T8 consume
// ambas formas.
func TestPRsRiskScoreEnListadoYDetalle(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedTriage(t, e)

	list := listarPRs(t, e, "")
	buscar := func(id int64) *prView {
		for i := range list {
			if list[i].ID == id {
				return &list[i]
			}
		}
		return nil
	}
	if v := buscar(fx.prs["top"].ID); v == nil || v.RiskScore == nil || *v.RiskScore != 90 {
		t.Errorf("top debe traer risk_score 90: %+v", v)
	}
	if v := buscar(fx.prs["none"].ID); v == nil || v.RiskScore != nil {
		t.Errorf("none (sin corridas) debe traer risk_score null: %+v", v)
	}
	// El detalle usa el mismo bloque pr: mismo score que el listado.
	resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/prs/%d", fx.prs["top"].ID), e.admin(t).cookie, e.admin(t).csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET detalle debe ser 200, fue %d", resp.StatusCode)
	}
	var out prDetailView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.PR.RiskScore == nil || *out.PR.RiskScore != 90 {
		t.Errorf("detalle debe espejar risk_score 90: %+v", out.PR.RiskScore)
	}
}

// sort=risk: score DESC, sin score al final, empate resuelto por updated_at
// DESC (fast se sembró después de slow). El default (sin sort) conserva el
// orden del SQL: updated_at DESC — que acá refleja TAMBIÉN los toques de
// UpdatePullRequestRiskScore (T3: pisa updated_at en cada score, en orden
// slow → fast → top → cerrado) y al PR "otro" del repo2, sembrado al final.
func TestPRsSortPorRiesgo(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedTriage(t, e)
	porNombre := func(nombres ...string) []int64 {
		out := make([]int64, 0, len(nombres))
		for _, n := range nombres {
			out = append(out, fx.prs[n].ID)
		}
		return out
	}

	defaultOrden := ids(listarPRs(t, e, ""), fx)
	if !mismos(defaultOrden, porNombre("cerrado", "top", "fast", "slow", "otro", "none")) {
		t.Errorf("default debe ser updated_at DESC (scores tocan updated_at), fue %v", defaultOrden)
	}
	riesgo := ids(listarPRs(t, e, "?sort=risk"), fx)
	if !mismos(riesgo, porNombre("top", "fast", "slow", "cerrado", "otro", "none")) {
		t.Errorf("sort=risk debe ser score DESC con nil al final (empate estable), fue %v", riesgo)
	}
	// sort=updated es explícitamente el default.
	if got := ids(listarPRs(t, e, "?sort=updated"), fx); !mismos(got, defaultOrden) {
		t.Errorf("sort=updated debe equivaler al default, fue %v", got)
	}
}

// state, repo y severity filtran sobre el listado completo; severity mira
// los conteos de la ÚLTIMA corrida (top tiene 1 high, fast 1 low). El orden
// relativo es el del SQL (ver TestPRsSortPorRiesgo para el porqué).
func TestPRsFiltrosStateRepoSeverity(t *testing.T) {
	e := newTestEnv(t, 5)
	fx := seedTriage(t, e)
	porNombre := func(nombres ...string) []int64 {
		out := make([]int64, 0, len(nombres))
		for _, n := range nombres {
			out = append(out, fx.prs[n].ID)
		}
		return out
	}

	abiertos := ids(listarPRs(t, e, "?state=open"), fx)
	if !mismos(abiertos, porNombre("top", "fast", "slow", "otro", "none")) {
		t.Errorf("state=open debe excluir al cerrado, fue %v", abiertos)
	}
	if got := ids(listarPRs(t, e, "?state=closed"), fx); !mismos(got, porNombre("cerrado")) {
		t.Errorf("state=closed debe traer solo el cerrado, fue %v", got)
	}
	if got := ids(listarPRs(t, e, fmt.Sprintf("?repo=%d", fx.repo2.ID)), fx); !mismos(got, porNombre("otro")) {
		t.Errorf("repo=repo2 debe traer solo su PR, fue %v", got)
	}
	if got := ids(listarPRs(t, e, "?severity=high"), fx); !mismos(got, porNombre("top")) {
		t.Errorf("severity=high debe traer solo top, fue %v", got)
	}
	if got := ids(listarPRs(t, e, "?severity=low"), fx); !mismos(got, porNombre("fast")) {
		t.Errorf("severity=low debe traer solo fast, fue %v", got)
	}
	if got := ids(listarPRs(t, e, "?severity=medium"), fx); len(got) != 0 {
		t.Errorf("severity=medium no matchea nadie, fue %v", got)
	}
	// Combinación: el triage pide abierto y riesgoso a la vez.
	combo := ids(listarPRs(t, e, "?state=open&sort=risk&severity=low"), fx)
	if !mismos(combo, porNombre("fast")) {
		t.Errorf("state=open&sort=risk&severity=low debe traer solo fast, fue %v", combo)
	}
}

// Parámetros desconocidos → 400 con mensaje, jamás ignore silencioso
// (decisión 8: fail closed).
func TestPRsQueryInvalidos400(t *testing.T) {
	e := newTestEnv(t, 5)
	seedTriage(t, e)
	a := e.admin(t)

	casos := []string{
		"?sort=popular",
		"?state=merged",
		"?repo=abc",
		"?repo=-1",
		"?severity=critical",
	}
	for _, query := range casos {
		resp := e.do(t, http.MethodGet, "/api/prs"+query, a.cookie, a.csrf, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s debe ser 400, fue %d", query, resp.StatusCode)
			continue
		}
		var out struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Error == "" {
			t.Errorf("%s debe responder {\"error\": \"...\"}: got %q err=%v", query, out.Error, err)
		}
	}
}
