package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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

	if opts := insertOpts("kind_sin_routing"); opts.Queue != QueueOps {
		t.Errorf("un kind sin routing va a ops: got %q", opts.Queue)
	}
}
