package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// Tests de repos conectados (guía §3.3/§3.5/§9.6): conectar exige proveedor
// review enabled (guarda de rol), la baja es flag (405 en DELETE) y la
// reconexión encola ReconcileJob.

// cleanupRepos borra los repos creados por el test (el store sí tiene
// DeleteRepository; lo que no hay es endpoint, §3.5).
func cleanupRepos(t *testing.T, e *testEnv, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		id := id
		t.Cleanup(func() { _, _ = e.st.Pool.Exec(context.Background(), "DELETE FROM repositories WHERE id = $1", id) })
	}
}

func TestReposCRUDYReconcile(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)
	e.createProvider(t, "review", true)    // guarda de rol satisfecha (§9.6)
	e.llm.embedDims = 1536                 // el guard de dims (§3.3) exige que el probe matchee el catálogo
	e.createProvider(t, "embedding", true) // §6 F4: conectar/reconectar encola IndexJob

	// Create: 201, la vista no filtra secretos.
	resp := e.do(t, http.MethodPost, "/api/repos", a.cookie, a.csrf, repoRequest{
		Vcs: "github", Owner: "acme", Name: "codeowl-e2e", ExternalID: time.Now().UnixNano()%1_000_000 + 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("conectar repo debe ser 201, fue %d", resp.StatusCode)
	}
	var repo repoView
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		t.Fatal(err)
	}
	cleanupRepos(t, e, repo.ID)
	if repo.Owner != "acme" || !repo.Enabled {
		t.Errorf("el repo debe nacer habilitado con sus datos: %+v", repo)
	}

	// Duplicado (misma clave natural vcs+external_id) → 409.
	resp = e.do(t, http.MethodPost, "/api/repos", a.cookie, a.csrf, repoRequest{
		Vcs: "github", Owner: "acme", Name: "codeowl-e2e", ExternalID: repo.ExternalID,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("repo duplicado debe ser 409, fue %d", resp.StatusCode)
	}

	// Update de config: language + review_drafts + chat_org_only.
	si := true
	resp = e.do(t, http.MethodPut, fmt.Sprintf("/api/repos/%d", repo.ID), a.cookie, a.csrf, repoUpdateRequest{
		Language: "en", ReviewDrafts: &si, ChatOrgOnly: &si,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update de config debe ser 200, fue %d", resp.StatusCode)
	}
	repo = repoView{}
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		t.Fatal(err)
	}
	if repo.Language != "en" || !repo.ReviewDrafts || !repo.ChatOrgOnly {
		t.Errorf("la config del repo debe persistir: %+v", repo)
	}

	// Disable: flag a false, sin enqueue de nada.
	kindsAntes, _ := e.queue.enqueued(t)
	no := false
	resp = e.do(t, http.MethodPut, fmt.Sprintf("/api/repos/%d", repo.ID), a.cookie, a.csrf, repoUpdateRequest{Enabled: &no})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable debe ser 200, fue %d", resp.StatusCode)
	}
	deshabilitado := repoView{}
	if err := json.NewDecoder(resp.Body).Decode(&deshabilitado); err != nil {
		t.Fatal(err)
	}
	if deshabilitado.Enabled {
		t.Error("disable debe dejar enabled=false")
	}

	// Re-enable: la guarda vuelve a pasar (proveedor enabled sigue ahí), la
	// reconexión encola el ReconcileJob (§3.5) y el IndexJob del repo
	// (§6 F4).
	si = true
	resp = e.do(t, http.MethodPut, fmt.Sprintf("/api/repos/%d", repo.ID), a.cookie, a.csrf, repoUpdateRequest{Enabled: &si})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-enable debe ser 200, fue %d", resp.StatusCode)
	}
	kinds, payloads := e.queue.enqueued(t)
	if len(kinds) != len(kindsAntes)+2 {
		t.Fatalf("la reconexión debe encolar ReconcileJob + IndexJob (§6 F4), hubo %d", len(kinds)-len(kindsAntes))
	}
	if kinds[len(kinds)-2] != jobs.KindReconcile {
		t.Errorf("el penúltimo job debe ser %q, fue %q", jobs.KindReconcile, kinds[len(kinds)-2])
	}
	var args jobs.ReconcileJobArgs
	if err := json.Unmarshal(payloads[len(kinds)-2], &args); err != nil {
		t.Fatal(err)
	}
	if args.RepositoryID != repo.ID {
		t.Errorf("el ReconcileJob debe apuntar al repo %d, apunta a %d", repo.ID, args.RepositoryID)
	}
	if kinds[len(kinds)-1] != jobs.KindIndex {
		t.Errorf("el último job debe ser %q, fue %q", jobs.KindIndex, kinds[len(kinds)-1])
	}
	var idxArgs jobs.IndexJobArgs
	if err := json.Unmarshal(payloads[len(kinds)-1], &idxArgs); err != nil {
		t.Fatal(err)
	}
	if idxArgs.RepositoryID != repo.ID {
		t.Errorf("el IndexJob debe apuntar al repo %d, apunta a %d", repo.ID, idxArgs.RepositoryID)
	}

	// List: el repo aparece con su estado.
	resp = e.do(t, http.MethodGet, "/api/repos", a.cookie, a.csrf, nil)
	var list []repoView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	var visto *repoView
	for i := range list {
		if list[i].ID == repo.ID {
			visto = &list[i]
		}
	}
	if visto == nil || !visto.Enabled {
		t.Errorf("el repo reconectado debe listarse habilitado: %+v", visto)
	}
}

// Guarda de rol (§9.6): sin proveedor review enabled, conectar da 422 con
// mensaje claro — y re-habilitar un repo deshabilitado también.
func TestReposGuardaSinProveedorReview(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)

	// Estado previo: apagar los review enabled que haya y restaurarlos al final.
	var previos []int64
	rows, err := e.st.Pool.Query(context.Background(),
		"SELECT id FROM llm_providers WHERE role = 'review' AND enabled")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		previos = append(previos, id)
	}
	rows.Close()
	if _, err := e.st.Pool.Exec(context.Background(),
		"UPDATE llm_providers SET enabled = false WHERE role = 'review'"); err != nil {
		t.Fatal(err)
	}
	if len(previos) > 0 {
		t.Cleanup(func() {
			_, _ = e.st.Pool.Exec(context.Background(),
				"UPDATE llm_providers SET enabled = true WHERE id = ANY($1)", previos)
		})
	}

	// Conectar sin proveedor review → 422.
	resp := e.do(t, http.MethodPost, "/api/repos", a.cookie, a.csrf, repoRequest{
		Vcs: "github", Owner: "acme", Name: "sin-guardia", ExternalID: 42,
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("conectar sin proveedor review debe ser 422, fue %d", resp.StatusCode)
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Error == "" {
		t.Errorf("el 422 debe traer un mensaje claro: %+v, err %v", out, err)
	}

	// Re-habilitar un repo deshabilitado sin proveedor review → 422 igual.
	repo, err := e.st.CreateRepository(context.Background(), store.CreateRepositoryParams{
		Vcs: "github", ExternalID: 424242, Owner: "acme", Name: "apagado",
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupRepos(t, e, repo.ID)
	if _, err := e.st.SetRepositoryEnabled(context.Background(), store.SetRepositoryEnabledParams{ID: repo.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	si := true
	if got := e.do(t, http.MethodPut, fmt.Sprintf("/api/repos/%d", repo.ID), a.cookie, a.csrf, repoUpdateRequest{Enabled: &si}).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("re-habilitar sin proveedor review debe ser 422, fue %d", got)
	}
	// Nada cambió: el repo sigue deshabilitado.
	if got, err := e.st.GetRepository(context.Background(), repo.ID); err != nil || got.Enabled {
		t.Errorf("la guarda rechazada no debe tocar el repo: enabled=%v, err %v", got.Enabled, err)
	}
}

// §3.5: "la baja es un flag, no un delete" — DELETE /api/repos/{id} es 405.
func TestReposDeleteEs405(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)
	if got := e.do(t, http.MethodDelete, "/api/repos/123", a.cookie, a.csrf, nil).StatusCode; got != http.StatusMethodNotAllowed {
		t.Errorf("DELETE de repo debe ser 405, fue %d", got)
	}
}

// resolverStub implementa repoResolver: fija la respuesta del adapter.
type resolverStub struct {
	stubVCS
	id        int64
	installed bool
	err       error
}

func (r *resolverStub) ResolveRepo(_ context.Context, _, _ string) (int64, bool, error) {
	return r.id, r.installed, r.err
}

// TestResolveRepo: el formulario de conexión resuelve el external_id de un
// repo (privado incluido) con la App — y obtiene el remedio claro cuando la
// App no está instalada (guía §3.3).
func TestResolveRepo(t *testing.T) {
	t.Run("resuelve el external_id", func(t *testing.T) {
		e := newTestEnvVCS(t, 5, &resolverStub{id: 1398958392, installed: true}, nil)
		a := e.admin(t)
		resp := e.do(t, http.MethodGet, "/api/repos/github/resolve?owner=acme&name=privado", a.cookie, a.csrf, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, querés 200", resp.StatusCode)
		}
		var out repoResolveView
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if !out.Installed || out.ExternalID != 1398958392 || out.FullName != "acme/privado" {
			t.Errorf("vista = %+v, querés installed con external_id 1398958392", out)
		}
	})

	t.Run("sin instalación da 404 con el remedio", func(t *testing.T) {
		e := newTestEnvVCS(t, 5, &resolverStub{installed: false}, nil)
		a := e.admin(t)
		resp := e.do(t, http.MethodGet, "/api/repos/github/resolve?owner=acme&name=privado", a.cookie, a.csrf, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, querés 404", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "instalala") {
			t.Errorf("el mensaje debe decir cómo remediarlo: %s", body)
		}
	})

	t.Run("falta owner o name da 400", func(t *testing.T) {
		e := newTestEnvVCS(t, 5, &resolverStub{installed: true}, nil)
		a := e.admin(t)
		if got := e.do(t, http.MethodGet, "/api/repos/github/resolve?name=solo", a.cookie, a.csrf, nil).StatusCode; got != http.StatusBadRequest {
			t.Errorf("status = %d, querés 400", got)
		}
	})

	t.Run("error del upstream da 502", func(t *testing.T) {
		e := newTestEnvVCS(t, 5, &resolverStub{err: fmt.Errorf("GitHub caído")}, nil)
		a := e.admin(t)
		if got := e.do(t, http.MethodGet, "/api/repos/github/resolve?owner=a&name=b", a.cookie, a.csrf, nil).StatusCode; got != http.StatusBadGateway {
			t.Errorf("status = %d, querés 502", got)
		}
	})
}

// TestRepoSaludYRevisarAhora (S1): los checks que explican "¿por qué no se
// revisó mi PR?" y la salida operativa de encolar la corrida manual.
func TestRepoSaludYRevisarAhora(t *testing.T) {
	e := newTestEnvVCS(t, 5, &resolverStub{id: 777, installed: true}, nil)
	a := e.admin(t)
	e.createProvider(t, "review", true)

	// Identidad única por corrida (convención de los fixtures): un external_id
	// fijo colisiona con la corrida anterior si el cleanup falló a mitad.
	resp := e.do(t, http.MethodPost, "/api/repos", a.cookie, a.csrf, repoRequest{
		Vcs: "github", Owner: "acme", Name: "salud", ExternalID: time.Now().UnixNano()%1_000_000 + 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("conectando el repo: %d", resp.StatusCode)
	}
	var repo repoView
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.st.Pool.Exec(context.Background(), "DELETE FROM river_job WHERE kind='review' AND args->>'pull_request_id' IN (SELECT id::text FROM pull_requests WHERE repository_id=$1)", repo.ID)
		_, _ = e.st.Pool.Exec(context.Background(),
			"DELETE FROM repositories WHERE id = $1", repo.ID)
	})

	// PR abierto con identidad completa: el objetivo del "revisar ahora".
	pr, err := e.st.UpsertPullRequest(context.Background(), store.UpsertPullRequestParams{
		RepositoryID: repo.ID, Number: 7, Author: "ana", State: "open",
		HeadSha: "head-salud", BaseRef: "main", BaseSha: "base-salud",
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("salud con todo verde", func(t *testing.T) {
		resp := e.do(t, http.MethodGet, fmt.Sprintf("/api/repos/%d/health", repo.ID), a.cookie, a.csrf, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		var h repoHealthView
		if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
			t.Fatal(err)
		}
		if h.AppInstalled == nil || !*h.AppInstalled {
			t.Errorf("app_installed = %v, querés true (resolver instalado)", h.AppInstalled)
		}
		if !h.HasReviewProvider {
			t.Error("hay un proveedor review creado: has_review_provider debe ser true")
		}
		if h.OpenPRs != 1 || h.LatestOpenPR == nil || h.LatestOpenPR.Number != 7 {
			t.Errorf("open_prs/latest_open_pr = %+v, querés el PR 7", h.LatestOpenPR)
		}
		if h.LatestOpenPR.HeadSha != "head-salud" || h.LatestOpenPR.BaseSha != "base-salud" {
			t.Errorf("la identidad del PR viaja incompleta: %+v", h.LatestOpenPR)
		}
	})

	t.Run("revisar ahora encola con la identidad del PR", func(t *testing.T) {
		antes := len(e.queue.kinds)
		resp := e.do(t, http.MethodPost, fmt.Sprintf("/api/repos/%d/review", repo.ID), a.cookie, a.csrf, nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d, querés 202", resp.StatusCode)
		}
		if len(e.queue.kinds) != antes+1 {
			t.Fatalf("encolados antes=%d después=%d", antes, len(e.queue.kinds))
		}
		if e.queue.kinds[len(e.queue.kinds)-1] != "review" {
			t.Errorf("kind = %q, querés review", e.queue.kinds[len(e.queue.kinds)-1])
		}
		var rja jobs.ReviewJobArgs
		if err := json.Unmarshal(e.queue.payloads[len(e.queue.payloads)-1], &rja); err != nil {
			t.Fatal(err)
		}
		if rja.PullRequestID != pr.ID || rja.HeadSha != "head-salud" || rja.BaseSha != "base-salud" {
			t.Errorf("args = %+v, querés la identidad del PR 7", rja)
		}
	})

	t.Run("sin PRs abiertos lo dice y no encola", func(t *testing.T) {
		if _, err := e.st.Pool.Exec(context.Background(),
			"UPDATE pull_requests SET state='closed' WHERE repository_id=$1 AND number=7", repo.ID); err != nil {
			t.Fatal(err)
		}
		antes := len(e.queue.kinds)
		resp := e.do(t, http.MethodPost, fmt.Sprintf("/api/repos/%d/review", repo.ID), a.cookie, a.csrf, nil)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, querés 409", resp.StatusCode)
		}
		if len(e.queue.kinds) != antes {
			t.Error("sin PR abierto no debe encolar nada")
		}
	})
}
