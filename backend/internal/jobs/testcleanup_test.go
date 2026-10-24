// Helpers de limpieza de los fixtures de test: los jobs que el test encola
// quedan en river_job y, sin borrarlos, la base de desarrollo termina con
// filas de test que el panel "Cola de Jobs" muestra como descartadas o en
// espera — ruido que no corresponde a ninguna corrida real.
package jobs

import (
	"context"
	"testing"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// limpiaJobs borra de river_job los jobs del test: por kind, por
// pull_request_id y por repository_id, más los huérfanos — jobs que apuntan
// a un repo o PR que ya no existe (el fixture se borró antes que el job).
// El cast de los ids va dentro de un CASE: un args sin esos campos (o con
// un valor no numérico) no debe romper el DELETE.
func limpiaJobs(t *testing.T, st *store.Store, kinds []string, prIDs []int64, repoIDs []int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `
		DELETE FROM river_job
		WHERE kind = ANY($1)
		   OR CASE WHEN args->>'pull_request_id' ~ '^[0-9]+$'
		           THEN (args->>'pull_request_id')::bigint END = ANY($2)
		   OR CASE WHEN args->>'repository_id' ~ '^[0-9]+$'
		           THEN (args->>'repository_id')::bigint END = ANY($3)
		   OR (CASE WHEN args->>'repository_id' ~ '^[0-9]+$'
		           THEN (args->>'repository_id')::bigint END IS NOT NULL
		       AND NOT EXISTS (SELECT 1 FROM repositories r
		                       WHERE r.id = (args->>'repository_id')::bigint))
		   OR (CASE WHEN args->>'pull_request_id' ~ '^[0-9]+$'
		           THEN (args->>'pull_request_id')::bigint END IS NOT NULL
		       AND NOT EXISTS (SELECT 1 FROM pull_requests p
		                       WHERE p.id = (args->>'pull_request_id')::bigint))`,
		kinds, prIDs, repoIDs); err != nil {
		t.Errorf("borrando los jobs de test: %v", err)
	}
}
