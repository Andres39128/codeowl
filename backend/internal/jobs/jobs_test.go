package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// Tests contra el Postgres de desarrollo (mismo patrón de store_test.go):
// requieren DATABASE_URL en el entorno (just test lo exporta del .env).
func testQueue(t *testing.T) (*RiverQueue, *store.Store) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no está seteado: saltando tests de integración")
	}
	st, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)
	if err := store.Migrate(url, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	q, err := New(context.Background(), st.Pool)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return q, st
}

// El constructor valida conectividad y arma el cliente sin workers (F0).
func TestNewRiverQueueConecta(t *testing.T) {
	q, _ := testQueue(t)
	ctx := context.Background()

	// F0: Start/Stop son el ciclo de vida del esqueleto (no procesan nada).
	if err := q.Start(ctx); err != nil {
		t.Errorf("Start del esqueleto F0 debe ser no-op exitoso: %v", err)
	}
	if err := q.Stop(ctx); err != nil {
		t.Errorf("Stop del esqueleto F0 debe ser no-op exitoso: %v", err)
	}
}

// Enqueue inserta una fila en river_job con el kind y args pedidos. El kind
// es único por corrida para poder verificar el count exacto.
func TestEnqueueInsertaRiverJob(t *testing.T) {
	q, st := testQueue(t)
	ctx := context.Background()

	kind := fmt.Sprintf("f0_nop_%d", time.Now().UnixNano())
	args := json.RawMessage(`{"ok":true}`)

	var antes int
	if err := st.Pool.QueryRow(ctx, "SELECT COUNT(1) FROM river_job WHERE kind = $1", kind).Scan(&antes); err != nil {
		t.Fatal(err)
	}

	if err := q.Enqueue(ctx, kind, args); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	var despues int
	if err := st.Pool.QueryRow(ctx, "SELECT COUNT(1) FROM river_job WHERE kind = $1", kind).Scan(&despues); err != nil {
		t.Fatal(err)
	}
	if despues != antes+1 {
		t.Errorf("Enqueue debe insertar exactamente una fila en river_job: antes=%d después=%d", antes, despues)
	}

	// La fila queda available (encolada, no corrida — F0 no tiene workers).
	// Args se comparan como JSON normalizado: river_job.args es jsonb y
	// Postgres lo reescribe en su forma canónica ({"ok": true}).
	var estado string
	var encodedArgs []byte
	if err := st.Pool.QueryRow(ctx,
		"SELECT state, args FROM river_job WHERE kind = $1 ORDER BY id DESC LIMIT 1", kind).Scan(&estado, &encodedArgs); err != nil {
		t.Fatal(err)
	}
	if estado != "available" {
		t.Errorf("el job encolado debe quedar available, quedó %q", estado)
	}
	var got, want any
	if err := json.Unmarshal(encodedArgs, &got); err != nil {
		t.Fatalf("args no son json: %v", err)
	}
	if err := json.Unmarshal(args, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("los args deben persistir tal cual: %s", encodedArgs)
	}
}

// Inputs inválidos: kind vacío y JSON malformado se rechazan sin insertar.
func TestEnqueueRechazaInputsInvalidos(t *testing.T) {
	q, _ := testQueue(t)
	ctx := context.Background()

	if err := q.Enqueue(ctx, "", json.RawMessage(`{}`)); err == nil {
		t.Error("Enqueue sin kind debe fallar")
	}
	if err := q.Enqueue(ctx, "kind_valido", json.RawMessage(`{no-json`)); err == nil {
		t.Error("Enqueue con args no-json debe fallar")
	}
}

// Unicidad del ReviewJob (§3.6.1.4): el encolado duplicado de una identidad
// activa se descarta (ByArgs + ByState sin completed); una identidad nueva
// (push) sí inserta.
func TestEnqueueReviewJobUnicoPorArgs(t *testing.T) {
	q, st := testQueue(t)
	ctx := context.Background()

	prID := time.Now().UnixNano()
	args, err := json.Marshal(ReviewJobArgs{PullRequestID: prID, HeadSha: "h1", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := q.Enqueue(ctx, KindReview, args); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}
	var n int
	if err := st.Pool.QueryRow(ctx,
		"SELECT COUNT(1) FROM river_job WHERE kind = $1 AND args = $2", KindReview, args).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("el duplicado de una identidad activa no debe insertar: n=%d", n)
	}

	// Identidad nueva (push): head distinto → inserta.
	args2, err := json.Marshal(ReviewJobArgs{PullRequestID: prID, HeadSha: "h2", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, KindReview, args2); err != nil {
		t.Fatalf("Enqueue identidad nueva: %v", err)
	}
	if err := st.Pool.QueryRow(ctx,
		"SELECT COUNT(1) FROM river_job WHERE kind = $1 AND args IN ($2, $3)", KindReview, args, args2).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("la identidad nueva (push) debe insertar: n=%d", n)
	}
}

// El ReviewJob encola a su cola con unicidad y tope de intentos (§3.6.1.4/
// §9.7); un kind sin routing va a ops.
func TestInsertOptsPorKind(t *testing.T) {
	opts := insertOpts(KindReview)
	if opts.Queue != QueueReview || opts.MaxAttempts != reviewMaxAttempts {
		t.Errorf("ReviewJob: cola e intentos de §9.7: got %+v", opts)
	}
	if !opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) != 5 {
		t.Errorf("ReviewJob: unicidad ByArgs con 5 estados activos: got %+v", opts.UniqueOpts)
	}
	for _, s := range []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	} {
		found := false
		for _, have := range opts.UniqueOpts.ByState {
			if have == s {
				found = true
			}
		}
		if !found {
			t.Errorf("falta el estado %s en ByState (River lo exige o §3.6 lo pide)", s)
		}
	}
	for _, s := range []rivertype.JobState{rivertype.JobStateCompleted, rivertype.JobStateDiscarded} {
		for _, have := range opts.UniqueOpts.ByState {
			if have == s {
				t.Errorf("el estado %s no debe bloquear la unicidad (§3.6.1.4: sin completed)", s)
			}
		}
	}

	// IndexJob (§9.6): comparte la cola review, el tope de intentos y la
	// unicidad "único mientras viva" del review — N triggers del mismo repo,
	// una sola corrida.
	if opts := insertOpts(KindIndex); opts.Queue != QueueReview || opts.MaxAttempts != reviewMaxAttempts {
		t.Errorf("IndexJob: cola e intentos de §9.6/§9.7: got %+v", opts)
	} else if !opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) != 5 {
		t.Errorf("IndexJob: unicidad ByArgs con 5 estados activos: got %+v", opts.UniqueOpts)
	}

	// MetricsJob (§6 F5): cola ops con reintentos baratos y SIN unicidad —
	// cada cierre encola el suyo y el recompute del worker es idempotente.
	if opts := insertOpts(KindMetrics); opts.Queue != QueueOps || opts.MaxAttempts != metricsMaxAttempts {
		t.Errorf("MetricsJob: cola ops con %d intentos: got %+v", metricsMaxAttempts, opts)
	} else if opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) != 0 {
		t.Errorf("MetricsJob: sin unicidad (cada cierre recomputa): got %+v", opts.UniqueOpts)
	}

	if opts := insertOpts("kind_sin_routing"); opts.Queue != QueueOps {
		t.Errorf("un kind sin routing va a ops: got %q", opts.Queue)
	}
}

// wSuspenderEmbedding apaga los proveedores embedding enabled de la BD
// compartida y devuelve la función que los restaura: la guarda del IndexJob
// es de EXISTENCIA (§9.6) — los heredados de otros tests deben salir del
// medio para poder probar el caso "sin proveedor".
func wSuspenderEmbedding(t *testing.T, st *store.Store) func() {
	t.Helper()
	ctx := context.Background()
	rows, err := st.Pool.Query(ctx,
		"SELECT id FROM llm_providers WHERE role = 'embedding' AND enabled")
	if err != nil {
		t.Fatal(err)
	}
	var previos []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		previos = append(previos, id)
	}
	rows.Close()
	if _, err := st.Pool.Exec(ctx,
		"UPDATE llm_providers SET enabled = false WHERE role = 'embedding' AND enabled"); err != nil {
		t.Fatal(err)
	}
	return func() {
		if len(previos) > 0 {
			_, _ = st.Pool.Exec(ctx,
				"UPDATE llm_providers SET enabled = true WHERE id = ANY($1)", previos)
		}
	}
}

// IndexJob (§6 F4/§9.6): la guarda de proveedor embedding decide el encolado
// — sin proveedor no hay fila NI error (la indexación queda latente) — y la
// unicidad por repo converge N triggers en una sola corrida viva, en la cola
// review con el tope de intentos.
func TestEnqueueIndexJobGuardYUnicidad(t *testing.T) {
	q, st := testQueue(t)
	ctx := context.Background()
	restaurar := wSuspenderEmbedding(t, st)
	defer restaurar()

	repoA, repoB := wNano(), wNano()
	countFilas := func(repoID int64) int {
		t.Helper()
		var n int
		if err := st.Pool.QueryRow(ctx,
			"SELECT COUNT(1) FROM river_job WHERE kind = $1 AND args = $2",
			KindIndex, fmt.Sprintf(`{"repository_id": %d}`, repoID)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Sin proveedor embedding: sin fila, sin error (§9.6).
	if err := EnqueueIndexJob(ctx, st, q, repoA); err != nil {
		t.Fatalf("la guarda sin proveedor no debe fallar: %v", err)
	}
	if n := countFilas(repoA); n != 0 {
		t.Errorf("sin proveedor embedding no debe encolar: n=%d", n)
	}

	// Con proveedor: una fila; el duplicado vivo se deduplica; otro repo
	// inserta su propia corrida.
	clave := bytes.Repeat([]byte{0xC3}, 32)
	apiKey, err := store.Encrypt(clave, []byte("sk-embed-test"))
	if err != nil {
		t.Fatal(err)
	}
	prov, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: "https://embed.test/v1", Model: fmt.Sprintf("embed-%d", wNano()),
		ApiKey: apiKey, Role: roleEmbedding, Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.DeleteLlmProvider(ctx, prov.ID)

	for i := 0; i < 2; i++ {
		if err := EnqueueIndexJob(ctx, st, q, repoA); err != nil {
			t.Fatalf("EnqueueIndexJob %d: %v", i, err)
		}
	}
	if n := countFilas(repoA); n != 1 {
		t.Errorf("el IndexJob vivo debe ser único por repo (§9.6): n=%d", n)
	}
	if err := EnqueueIndexJob(ctx, st, q, repoB); err != nil {
		t.Fatalf("EnqueueIndexJob de otro repo: %v", err)
	}
	if n := countFilas(repoB); n != 1 {
		t.Errorf("otro repo inserta su propia corrida: n=%d", n)
	}

	// La fila vive en la cola review con el tope de intentos (§9.6/§9.7).
	var cola string
	var intentos int
	if err := st.Pool.QueryRow(ctx,
		"SELECT queue, max_attempts FROM river_job WHERE kind = $1 AND args = $2 LIMIT 1",
		KindIndex, fmt.Sprintf(`{"repository_id": %d}`, repoB)).Scan(&cola, &intentos); err != nil {
		t.Fatal(err)
	}
	if cola != QueueReview || intentos != reviewMaxAttempts {
		t.Errorf("el IndexJob comparte la cola review con %d intentos: cola=%q intentos=%d",
			reviewMaxAttempts, cola, intentos)
	}
}

// CancelPendingByPR (§3.5): descarta los jobs vivos de review y chat del PR
// pedido (quedan cancelled) y deja intactos los de otros PRs; un PR sin
// jobs devuelve 0 sin error.
func TestCancelPendingByPR(t *testing.T) {
	q, st := testQueue(t)
	ctx := context.Background()

	prA, prB := wNano(), wNano()
	encolar := func(kind string, prID int64, raw json.RawMessage) {
		t.Helper()
		if err := q.Enqueue(ctx, kind, raw); err != nil {
			t.Fatalf("Enqueue %s pr=%d: %v", kind, prID, err)
		}
	}
	rawA, err := json.Marshal(ReviewJobArgs{PullRequestID: prA, HeadSha: "h1", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	encolar(KindReview, prA, rawA)
	rawChat, err := json.Marshal(ChatJobArgs{
		RepositoryID: 1, PullRequestID: prA, ParentCommentID: "p1",
		CommentBody: "b", CommentAuthor: "a", HeadSha: "h1", BaseSha: "b1", Language: "es",
	})
	if err != nil {
		t.Fatal(err)
	}
	encolar(KindChat, prA, rawChat)
	rawB, err := json.Marshal(ReviewJobArgs{PullRequestID: prB, HeadSha: "h1", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	encolar(KindReview, prB, rawB)

	descartados, err := q.CancelPendingByPR(ctx, prA)
	if err != nil {
		t.Fatalf("CancelPendingByPR: %v", err)
	}
	if descartados != 2 {
		t.Errorf("el PR A tiene 2 jobs vivos (review+chat): descartados=%d", descartados)
	}

	estadoDe := func(kind string, prID int64) string {
		t.Helper()
		var estado string
		if err := st.Pool.QueryRow(ctx,
			`SELECT state FROM river_job WHERE kind = $1 AND args->>'pull_request_id' = $2
			 ORDER BY id DESC LIMIT 1`,
			kind, strconv.FormatInt(prID, 10)).Scan(&estado); err != nil {
			t.Fatalf("estado de %s pr=%d: %v", kind, prID, err)
		}
		return estado
	}
	if got := estadoDe(KindReview, prA); got != "cancelled" {
		t.Errorf("el review del PR A debe quedar cancelled: got %q", got)
	}
	if got := estadoDe(KindChat, prA); got != "cancelled" {
		t.Errorf("el chat del PR A debe quedar cancelled: got %q", got)
	}
	if got := estadoDe(KindReview, prB); got != "available" {
		t.Errorf("el review del PR B debe seguir available: got %q", got)
	}

	// PR sin jobs vivos: 0 y sin error.
	if n, err := q.CancelPendingByPR(ctx, wNano()); err != nil || n != 0 {
		t.Errorf("un PR sin jobs descarta 0 sin error: n=%d err=%v", n, err)
	}
}

// MetricsJob (§6 F5): cada cierre encola el suyo — SIN unicidad, dos
// encolados del mismo PR insertan dos filas — en la cola ops con 3 intentos.
func TestEnqueueMetricsJobOps(t *testing.T) {
	q, st := testQueue(t)
	ctx := context.Background()

	repoID, prID := wNano(), wNano()
	args := fmt.Sprintf(`{"repository_id": %d, "pull_request_id": %d}`, repoID, prID)
	for i := 0; i < 2; i++ {
		if err := EnqueueMetricsJob(ctx, q, repoID, prID); err != nil {
			t.Fatalf("EnqueueMetricsJob %d: %v", i, err)
		}
	}
	var n, intentos int
	var cola string
	if err := st.Pool.QueryRow(ctx,
		`SELECT COUNT(1), MAX(queue), MAX(max_attempts) FROM river_job
		 WHERE kind = $1 AND args = $2`, KindMetrics, args).Scan(&n, &cola, &intentos); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("el MetricsJob no lleva unicidad: cada cierre inserta la suya (n=%d)", n)
	}
	if cola != QueueOps || intentos != metricsMaxAttempts {
		t.Errorf("el MetricsJob va a ops con %d intentos: cola=%q intentos=%d", metricsMaxAttempts, cola, intentos)
	}
}
