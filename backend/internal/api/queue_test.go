package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Tests del panel de cola (guía §9.9): GET /api/jobs agrupa river_job por
// (kind, state) con cantidad y última creación; visible para cualquier
// usuario autenticado (§3.4).

func TestJobsPanelAgrupaPorKindYEstado(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)

	// Fixture propio con kinds únicos: dos review-test (pending + running) y
	// un cleanup-test discarded. river_job es de River: insert directo (el
	// panel lee la tabla real, no un wrapper).
	sufijo := time.Now().UnixNano()
	k1 := fmt.Sprintf("panel-review-%d", sufijo)
	k2 := fmt.Sprintf("panel-cleanup-%d", sufijo)
	insertar := func(kind, state string) {
		t.Helper()
		// finalized: los estados finales (discarded acá) exigen finalized_at
		// NOT NULL (constraint de River).
		var finalized any
		switch state {
		case "discarded", "completed", "cancelled":
			finalized = time.Now()
		}
		if _, err := e.st.Pool.Exec(context.Background(),
			`INSERT INTO river_job (kind, queue, state, max_attempts, args, finalized_at)
			 VALUES ($1, 'ops', $2, 1, '{}'::jsonb, $3)`,
			kind, state, finalized); err != nil {
			t.Fatalf("insertando job de prueba (%s/%s): %v", kind, state, err)
		}
	}
	insertar(k1, "pending")
	insertar(k1, "running")
	insertar(k2, "discarded")
	t.Cleanup(func() {
		_, _ = e.st.Pool.Exec(context.Background(),
			"DELETE FROM river_job WHERE kind IN ($1, $2)", k1, k2)
	})

	resp := e.do(t, http.MethodGet, "/api/jobs", a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/jobs debe ser 200, fue %d", resp.StatusCode)
	}
	var rows []jobQueueRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	buscar := func(kind, state string) *jobQueueRow {
		for i := range rows {
			if rows[i].Kind == kind && rows[i].State == state {
				return &rows[i]
			}
		}
		return nil
	}

	pending := buscar(k1, "pending")
	running := buscar(k1, "running")
	discarded := buscar(k2, "discarded")
	if pending == nil || pending.Count != 1 {
		t.Errorf("debe haber un grupo %s/pending con 1: %+v", k1, pending)
	}
	if running == nil || running.Count != 1 {
		t.Errorf("debe haber un grupo %s/running con 1: %+v", k1, running)
	}
	if discarded == nil || discarded.Count != 1 {
		t.Errorf("debe haber un grupo %s/discarded con 1: %+v", k2, discarded)
	}
	// La última creación es reciente (no epoch cero).
	if pending != nil && time.Since(pending.LatestCreatedAt) > time.Minute {
		t.Errorf("latest_created_at debe ser la creación reciente del job: %v", pending.LatestCreatedAt)
	}
}

// El member consulta la cola (§3.4: opera el triage y consulta el estado).
func TestJobsVisibleParaMember(t *testing.T) {
	e := newTestEnv(t, 5)
	m := e.member(t)
	if got := e.do(t, http.MethodGet, "/api/jobs", m.cookie, m.csrf, nil).StatusCode; got != http.StatusOK {
		t.Errorf("GET /api/jobs como member debe ser 200, fue %d", got)
	}
	// Sin sesión → 401 como cualquier endpoint.
	if got := e.do(t, http.MethodGet, "/api/jobs", nil, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("GET /api/jobs sin sesión debe ser 401, fue %d", got)
	}
}
