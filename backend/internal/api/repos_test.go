package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
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
	e.createProvider(t, "review", true) // guarda de rol satisfecha (§9.6)

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

	// Re-enable: la guarda vuelve a pasar (proveedor enabled sigue ahí) y la
	// reconexión encola el ReconcileJob del repo (§3.5).
	si = true
	resp = e.do(t, http.MethodPut, fmt.Sprintf("/api/repos/%d", repo.ID), a.cookie, a.csrf, repoUpdateRequest{Enabled: &si})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-enable debe ser 200, fue %d", resp.StatusCode)
	}
	kinds, payloads := e.queue.enqueued(t)
	if len(kinds) != len(kindsAntes)+1 {
		t.Fatalf("la reconexión debe encolar exactamente un job, hubo %d", len(kinds)-len(kindsAntes))
	}
	last := len(kinds) - 1
	if kinds[last] != jobs.KindReconcile {
		t.Errorf("el job encolado debe ser %q, fue %q", jobs.KindReconcile, kinds[last])
	}
	var args jobs.ReconcileJobArgs
	if err := json.Unmarshal(payloads[last], &args); err != nil {
		t.Fatal(err)
	}
	if args.RepositoryID != repo.ID {
		t.Errorf("el ReconcileJob debe apuntar al repo %d, apunta a %d", repo.ID, args.RepositoryID)
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
