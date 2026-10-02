// Package jobs es el wrapper fino de la cola (mapa: backend.jobs): la
// interfaz JobQueue propia aísla a River — único paquete que lo importa;
// reemplazar la cola = reescribir solo acá. F1: tipos de job del dominio
// (ReviewJob, CleanupJob, RotationJob, ReconcileJob) y workers River
// (workers.go); los handlers de la API encolan por la interfaz, jamás
// importan River directo.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

// Kinds de los jobs del dominio (mapa: backend.jobs). El que aún no tiene
// worker es ChatJob (F2) — su cola ya está dimensionada por CHAT_CONCURRENCY.
const (
	KindReview    = "review"
	KindChat      = "chat" // F2
	KindCleanup   = "cleanup"
	KindRotation  = "rotation"
	KindReconcile = "reconcile"
)

// Colas River (§9.6): review y chat corren con slots propios — el SLO de
// respuesta del chat no espera detrás de reviews largas. Ops (limpieza,
// rotación, reconciliación) corre serial: son raras, baratas y ninguna pisa
// a otra.
const (
	QueueReview = "review"
	QueueChat   = "chat"
	QueueOps    = "ops"
)

// reviewMaxAttempts es el tope de intentos del ReviewJob (§9.7: máximo de
// intentos con el backoff exponencial de River — 5 intentos ≈ 16 min de
// ventana). Al agotarlos, River lo deja discarded y el worker marca la
// corrida failed.
// ponytail: constante, no config — subirla a Stage2 si ops pide afinarla.
const reviewMaxAttempts = 5

// ReviewJobArgs son los argumentos del ReviewJob. (HeadSha, BaseSha) es la
// identidad de la corrida (§3.6.1): push y retarget la cambian y marcan
// stale las corridas viejas.
type ReviewJobArgs struct {
	RepositoryID  int64  `json:"repository_id,omitempty"`
	PullRequestID int64  `json:"pull_request_id"`
	HeadSha       string `json:"head_sha"`
	BaseSha       string `json:"base_sha"`
}

// Kind registra el tipo de args ante River (mismo kind que usa la API al
// encolar por JSON crudo).
func (ReviewJobArgs) Kind() string { return KindReview }

// CleanupJobArgs son los argumentos del CleanupJob (§9.11): sin parámetros —
// la retención la trae la config del worker.
type CleanupJobArgs struct{}

func (CleanupJobArgs) Kind() string { return KindCleanup }

// RotationJobArgs son los argumentos del RotationJob (§9.2): sin
// parámetros — las claves (nueva y previa) viven en el env del worker.
type RotationJobArgs struct{}

func (RotationJobArgs) Kind() string { return KindRotation }

// ReconcileJobArgs son los argumentos del ReconcileJob (§3.5): el repo a
// reconciliar tras una reconexión.
type ReconcileJobArgs struct {
	RepositoryID int64 `json:"repository_id"`
}

func (ReconcileJobArgs) Kind() string { return KindReconcile }

// Worker es lo que Register acepta: un worker River concreto de este
// paquete. Un método de interfaz no puede ser genérico, así que cada worker
// implementa register con river.AddWorkerSafely y la interfaz queda libre de
// tipos parametrizados.
type Worker interface {
	register(*river.Workers) error
}

// JobQueue es el contrato que consumen los handlers de la api y el worker.
// Los handlers jamás importan River directo (mapa: backend.jobs).
type JobQueue interface {
	// Enqueue encola un job por kind con argumentos JSON crudos. El ReviewJob
	// se encola único por PR (§3.6.1.4) — la unicidad la aplica la cola.
	Enqueue(ctx context.Context, kind string, args json.RawMessage) error

	// Register agrega workers de dominio; debe llamarse antes de Start. La
	// API no registra workers: su cliente es insert-only.
	Register(workers ...Worker) error

	// Start enciende el procesamiento (solo con workers registrados); Stop
	// lo drena ordenado (§9.12).
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Options dimensiona las colas del worker (§9.6). Cero = defaults.
type Options struct {
	ReviewConcurrency int // MaxWorkers de la cola review (default 2)
	ChatConcurrency   int // MaxWorkers de la cola chat (default 2)
}

// RiverQueue implementa JobQueue sobre River + pgxv5. Mantiene dos clientes:
// uno insert-only (la API y los tests encolan con él desde F0) y, si se
// registraron workers, uno que procesa — lo arma Start y lo drena Stop.
// El pool es el de la store (una sola conexión lógica al server).
type RiverQueue struct {
	pool    *pgxpool.Pool
	client  *river.Client[pgx.Tx] // insert-only: Enqueue
	working *river.Client[pgx.Tx] // con workers: Start/Stop

	opts       Options
	workers    *river.Workers
	periodics  []*river.PeriodicJob
	registered bool
}

// New construye el cliente insert-only y valida que la BD responde. opts
// dimensiona las colas para cuando se registren workers (solo el worker del
// dominio los pasa; API y tests usan el default).
func New(ctx context.Context, pool *pgxpool.Pool, opts ...Options) (*RiverQueue, error) {
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("la base de datos no responde para la cola: %w", err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("construyendo el cliente de River: %w", err)
	}
	o := Options{}
	if len(opts) > 0 {
		o = opts[0]
	}
	// §9.6: defaults de concurrencia; un valor inválido nunca cuelga la cola
	// (mismo criterio que llm.New).
	if o.ReviewConcurrency < 1 {
		o.ReviewConcurrency = 2
	}
	if o.ChatConcurrency < 1 {
		o.ChatConcurrency = 2
	}
	return &RiverQueue{pool: pool, client: client, opts: o, workers: river.NewWorkers()}, nil
}

// rawJobArgs adapta un kind + JSON crudo al JobArgs de River. La API encola
// por JSON crudo (su cliente es insert-only y no valida kinds); el worker,
// con workers registrados, rechaza kinds desconocidos en su propio Enqueue:
// guard gratis.
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

// reviewUniqueStates fija la unicidad del ReviewJob (§3.6.1.4): único por
// args (identidad del PR y de la corrida) mientras viva en cualquier estado
// activo. completed queda FUERA a propósito — completado no bloquea la
// siguiente corrida (el re-encolo transaccional de §3.6.1.5 vive en el
// pipeline). Al customizar ByState, River exige incluir pending, scheduled,
// available y running; retryable se mantiene: un reintento en curso también
// bloquea duplicados.
func reviewUniqueStates() []rivertype.JobState {
	return []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}
}

// insertOpts resuelve la cola y las opciones por kind: review lleva
// unicidad y tope de intentos (§3.6.1.4/§9.7); el resto va a ops.
func insertOpts(kind string) *river.InsertOpts {
	opts := &river.InsertOpts{Queue: QueueOps}
	switch kind {
	case KindReview:
		opts.Queue = QueueReview
		opts.MaxAttempts = reviewMaxAttempts
		opts.UniqueOpts = river.UniqueOpts{ByArgs: true, ByState: reviewUniqueStates()}
	case KindChat:
		opts.Queue = QueueChat
	}
	return opts
}

// Enqueue inserta el job en river_job (queda available). Falla con kind
// vacío; JSON inválido lo detecta el marshal de River. El encolado duplicado
// de un ReviewJob activo se descarta en silencio (River devuelve el job
// existente: unicidad §3.6.1.4).
func (q *RiverQueue) Enqueue(ctx context.Context, kind string, args json.RawMessage) error {
	if kind == "" {
		return errors.New("encolando job sin kind")
	}
	if !json.Valid(args) {
		return fmt.Errorf("args inválidos para el job %q: json malformado", kind)
	}
	if _, err := q.client.Insert(ctx, rawJobArgs{kind: kind, args: args}, insertOpts(kind)); err != nil {
		return fmt.Errorf("encolando job %q: %w", kind, err)
	}
	return nil
}

// Register agrega workers de dominio al cliente que Start arrancará. Debe
// llamarse antes de Start; la API nunca lo llama (insert-only).
func (q *RiverQueue) Register(workers ...Worker) error {
	for _, w := range workers {
		if err := w.register(q.workers); err != nil {
			return fmt.Errorf("registrando worker: %w", err)
		}
	}
	q.registered = true
	return nil
}

// RegisterPeriodic agenda jobs periódicos (los arma el paquete: hoy, el
// CleanupJob diario de §9.11). Uso interno del wiring del worker.
func (q *RiverQueue) RegisterPeriodic(p ...*river.PeriodicJob) {
	q.periodics = append(q.periodics, p...)
}

// Start enciende el procesamiento. Sin workers registrados es el no-op de
// F0 (la API encola pero no procesa); con ellos, arma el cliente que
// procesa las colas y lo arranca.
func (q *RiverQueue) Start(ctx context.Context) error {
	if !q.registered {
		slog.Info("cola River lista sin workers (insert-only: encola pero no procesa)")
		return nil
	}
	client, err := river.NewClient(riverpgxv5.New(q.pool), &river.Config{
		Workers: q.workers,
		Queues: map[string]river.QueueConfig{
			QueueReview: {MaxWorkers: q.opts.ReviewConcurrency},
			QueueChat:   {MaxWorkers: q.opts.ChatConcurrency},
			QueueOps:    {MaxWorkers: 1},
		},
		PeriodicJobs: q.periodics,
		// Un review > 1h no debe ser "rescatado" en vuelo (duplicaría la
		// corrida): techo generoso por encima de los timeouts del pipeline.
		RescueStuckJobsAfter: 4 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("construyendo el cliente de River con workers: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("arrancando el procesamiento de jobs: %w", err)
	}
	q.working = client
	slog.Info("cola River procesando jobs",
		"review_concurrency", q.opts.ReviewConcurrency, "chat_concurrency", q.opts.ChatConcurrency)
	return nil
}

// Stop drena el cliente que procesa (§9.12: deja de tomar jobs nuevos y
// termina el job en curso). Sin workers no hay nada que drenar.
func (q *RiverQueue) Stop(ctx context.Context) error {
	if q.working == nil {
		return nil
	}
	return q.working.Stop(ctx)
}

// cleanupPeriodic agenda el CleanupJob diario (§9.11). El scheduler corre en
// el líder (single worker). RunOnStart: un arranque nuevo limpia lo que dejó
// un worker crasheado — la retención no espera 24 h.
func CleanupPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) {
			return CleanupJobArgs{}, &river.InsertOpts{Queue: QueueOps}
		},
		&river.PeriodicJobOpts{ID: "cleanup-diario", RunOnStart: true},
	)
}
