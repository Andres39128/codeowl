// Package jobs es el wrapper fino de la cola (mapa: backend.jobs): la
// interfaz JobQueue propia aísla a River — único paquete que lo importa;
// reemplazar la cola = reescribir solo acá. F0 esqueleto: sin workers ni
// tipos de job (los 7 del dominio llegan desde F1 — no se inventan acá).
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// KindReview identifica el ReviewJob (guía §3.5/§3.6): pipeline de revisión
// de un PR. El webhook lo encola; el worker registra el worker con el mismo
// kind.
const KindReview = "review"

// ReviewJobArgs son los argumentos del ReviewJob. (HeadSha, BaseSha) es la
// identidad de la corrida (§3.6.1): push y retarget la cambian y marcan
// stale las corridas viejas.
type ReviewJobArgs struct {
	PullRequestID int64  `json:"pull_request_id"`
	HeadSha       string `json:"head_sha"`
	BaseSha       string `json:"base_sha"`
}

// JobQueue es el contrato que consumen los handlers de la api y el worker.
// Los handlers jamás importan River directo (mapa: backend.jobs).
type JobQueue interface {
	// Enqueue encola un job por kind con argumentos JSON crudos. Los tipos
	// del dominio (ReviewJob, IndexJob...) definen sus kinds y args desde F1.
	Enqueue(ctx context.Context, kind string, args json.RawMessage) error

	// Start enciende el procesamiento; Stop lo drena ordenado.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// RiverQueue implementa JobQueue sobre River + pgxv5. El pool es el de la
// store (una sola conexión lógica al server; River no corre SQL propio acá
// más allá del suyo de cola).
type RiverQueue struct {
	client *river.Client[pgx.Tx]
}

// New construye el cliente River y valida que la BD responde. Sin Workers ni
// Queues en config: F0 no procesa jobs — insert-only (el Start de River sin
// workers configurados rechaza arrancar, que es exactamente lo que queremos
// en F0). Desde F1, NewWorker... alimenta river.Config.Workers.
func New(ctx context.Context, pool *pgxpool.Pool) (*RiverQueue, error) {
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("la base de datos no responde para la cola: %w", err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("construyendo el cliente de River: %w", err)
	}
	return &RiverQueue{client: client}, nil
}

// rawJobArgs adapta un kind + JSON crudo al JobArgs de River. Con Workers nil
// (F0) River no valida kinds registrados: cualquier kind encola — y desde F1,
// con workers registrados, River rechazará kinds desconocidos: guard gratis.
type rawJobArgs struct {
	kind string
	args json.RawMessage
}

func (a rawJobArgs) Kind() string { return a.kind }

// MarshalJSON entrega los args tal cual: el JSON crudo es el payload.
func (a rawJobArgs) MarshalJSON() ([]byte, error) {
	if len(a.args) == 0 {
		return []byte("{}"), nil
	}
	return a.args, nil
}

// Enqueue inserta el job en river_job (queda available). Falla con kind
// vacío; JSON inválido lo detecta el marshal de River.
func (q *RiverQueue) Enqueue(ctx context.Context, kind string, args json.RawMessage) error {
	if kind == "" {
		return errors.New("encolando job sin kind")
	}
	if !json.Valid(args) {
		return fmt.Errorf("args inválidos para el job %q: json malformado", kind)
	}
	if _, err := q.client.Insert(ctx, rawJobArgs{kind: kind, args: args}, nil); err != nil {
		return fmt.Errorf("encolando job %q: %w", kind, err)
	}
	return nil
}

// Start no procesa nada en F0 (no hay workers que arrancar — River exigiría
// Queues/Workers configurados). Esqueleto honesto: no-op documentado; desde
// F1 delega en client.Start.
func (q *RiverQueue) Start(_ context.Context) error {
	slog.Info("cola River lista sin workers (F0: no procesa jobs)")
	return nil
}

// Stop no tiene nada que drenar en F0 (nada arrancó). Desde F1 delega en
// client.Stop con su gracia de apagado.
func (q *RiverQueue) Stop(_ context.Context) error {
	return nil
}
