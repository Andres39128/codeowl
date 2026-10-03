// Workers River del dominio (mapa: backend.jobs, F1): ReviewJob ejecuta el
// pipeline de revisión (§3.6), CleanupJob la retención (§9.11), RotationJob
// el re-cifrado al rotar la master key (§9.2) y ReconcileJob la
// reconciliación de PRs al reconectar un repo (§3.5, sin LLM). Ninguno
// consume el gateway LLM directo: el único que habla con proveedores es el
// pipeline de review (internal/review).
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// workdirPrefix nombra los workdirs de jobs (§9.11: rev-* en el root).
const workdirPrefix = "rev-"

// defaultWorkdirRoot es el root de los workdirs de jobs (§9.11: /var/tmp).
const defaultWorkdirRoot = "/var/tmp"

// workdirOrphanAge es la edad a partir de la cual un workdir del root es
// huérfano (worker crasheado a mitad de job, §9.11) y el CleanupJob lo borra.
// ponytail: 1h fija — las reviews corren en minutos; si alguna llega a la
// hora, el cleanup del arranque la borra en caliente y la corrida declara su
// gap de cobertura SAST.
const workdirOrphanAge = time.Hour

// workdirRoot resuelve el root efectivo (los tests lo pisan).
func workdirRoot(override string) string {
	if override != "" {
		return override
	}
	return defaultWorkdirRoot
}

// ---------------------------------------------------------------------------
// ReviewJobWorker — pipeline de revisión de un PR (§3.6/§9.7)
// ---------------------------------------------------------------------------

// ReviewJobWorker corre una corrida de review: clona el PR al workdir del
// job y delega en review.Run (diff → SAST → Reviewer → dedup → Summarizer →
// publicación en dos fases). Un error reintenta con el backoff de River; al
// agotar los intentos, la corrida queda failed (§9.7) y el job discarded.
type ReviewJobWorker struct {
	river.WorkerDefaults[ReviewJobArgs]

	Store    *store.Store
	Gateway  review.Gateway  // *llm.Gateway la satisface
	Analyzer review.Analyzer // *analyze.Runner la satisface
	Provider vcs.VCSProvider
	Config   review.Config

	WorkdirRoot string // default /var/tmp (los tests usan un tmp propio)
}

func (w *ReviewJobWorker) register(b *river.Workers) error { return river.AddWorkerSafely(b, w) }

// Work ejecuta la corrida. El workdir vive solo durante el job: se crea con
// el ID del job y se borra al salir,成功 o fallo.
func (w *ReviewJobWorker) Work(ctx context.Context, job *river.Job[ReviewJobArgs]) error {
	args := job.Args
	workdir, err := os.MkdirTemp(workdirRoot(w.WorkdirRoot), fmt.Sprintf("%s%d-", workdirPrefix, job.ID))
	if err != nil {
		return fmt.Errorf("creando el workdir del job %d: %w", job.ID, err)
	}
	defer os.RemoveAll(workdir)

	pr, err := w.Store.GetPullRequest(ctx, args.PullRequestID)
	if err != nil {
		return w.failFinal(ctx, job, fmt.Errorf("cargando el PR %d: %w", args.PullRequestID, err))
	}
	repoID := args.RepositoryID
	if repoID == 0 {
		repoID = pr.RepositoryID // encolados viejos sin repository_id
	}
	repo, err := w.Store.GetRepository(ctx, repoID)
	if err != nil {
		return w.failFinal(ctx, job, fmt.Errorf("cargando el repo %d: %w", repoID, err))
	}

	if err := w.Provider.FetchPR(ctx, &repo, &pr, workdir); err != nil {
		return w.failFinal(ctx, job, fmt.Errorf("clonando el PR %d: %w", pr.Number, err))
	}

	res, err := review.Run(ctx, w.Config, w.Store, w.Gateway, w.Analyzer, w.Provider, review.ReviewInput{
		PullRequestID: args.PullRequestID,
		RepositoryID:  repoID,
		HeadSHA:       args.HeadSha,
		BaseSHA:       args.BaseSha,
		Workdir:       workdir,
	})
	if err != nil {
		return w.failFinal(ctx, job, err)
	}
	slog.Info("review job completado", "job", job.ID, "pr", pr.Number, "review", res.Status,
		"findings", res.FindingsCount)
	return nil
}

// failFinal marca la corrida failed cuando el error es del último intento
// (§9.7: intentos agotados → corrida failed y job discarded — jamás una fila
// running huérfana). Antes del último, el error solo pide el reintento. El
// re-edit best-effort del resumen ya lo maneja el pipeline cuando la
// publicación alcanzó a arrancar (§9.7).
func (w *ReviewJobWorker) failFinal(ctx context.Context, job *river.Job[ReviewJobArgs], cause error) error {
	if job.Attempt < job.MaxAttempts {
		return cause
	}
	rev, err := w.Store.GetLatestReviewByPR(ctx, job.Args.PullRequestID)
	if err == nil && rev.Status == review.StatusRunning &&
		rev.HeadSha == job.Args.HeadSha && rev.BaseSha == job.Args.BaseSha {
		if _, uerr := w.Store.UpdateReviewStatus(ctx, store.UpdateReviewStatusParams{
			ID: rev.ID, Status: review.StatusFailed,
		}); uerr != nil {
			slog.Warn("review job: no se pudo marcar la corrida failed al agotar intentos",
				"review", rev.ID, "error", uerr)
		}
	}
	return cause
}

// ---------------------------------------------------------------------------
// ChatJobWorker — respuesta de chat a menciones @bot (§3.5/§6 F2)
// ---------------------------------------------------------------------------

// ChatJobWorker atiende una mención @bot: delega en review.HandleChat
// (parseo del comando, LLM, publicación en el hilo con idempotencia por
// comentario padre) y, si el comando fue /review sobre un PR abierto, encola
// el ReviewJob con la identidad ACTUAL del PR (§3.6 — misma unicidad de
// siempre; el webhook jamás llamó LLM, §3.5).
type ChatJobWorker struct {
	river.WorkerDefaults[ChatJobArgs]

	Store    *store.Store
	Gateway  review.Gateway // *llm.Gateway la satisface
	Provider vcs.VCSProvider
	Queue    JobQueue // encola el ReviewJob del /review
	Config   review.ChatConfig
}

func (w *ChatJobWorker) register(b *river.Workers) error { return river.AddWorkerSafely(b, w) }

func (w *ChatJobWorker) Work(ctx context.Context, job *river.Job[ChatJobArgs]) error {
	args := job.Args
	res, err := review.HandleChat(ctx, w.Config, w.Store, w.Gateway, w.Provider, review.ChatInput{
		PullRequestID:   args.PullRequestID,
		RepositoryID:    args.RepositoryID,
		ParentCommentID: args.ParentCommentID,
		CommentBody:     args.CommentBody,
		CommentAuthor:   args.CommentAuthor,
		HeadSHA:         args.HeadSha,
		BaseSHA:         args.BaseSha,
		Language:        args.Language,
	})
	if err != nil {
		return err
	}
	if res.EnqueueReview {
		// Identidad actual del PR (§3.6): un push o retarget entre el
		// comentario y esta corrida no revisa un diff viejo.
		pr, err := w.Store.GetPullRequest(ctx, args.PullRequestID)
		if err != nil {
			return fmt.Errorf("cargando el PR %d para el /review: %w", args.PullRequestID, err)
		}
		bargs, err := json.Marshal(ReviewJobArgs{
			RepositoryID:  args.RepositoryID,
			PullRequestID: args.PullRequestID,
			HeadSha:       pr.HeadSha,
			BaseSha:       pr.BaseSha,
		})
		if err != nil {
			return fmt.Errorf("serializando args del ReviewJob: %w", err)
		}
		if err := w.Queue.Enqueue(ctx, KindReview, bargs); err != nil {
			return fmt.Errorf("encolando ReviewJob del /review: %w", err)
		}
	}
	slog.Info("chat job completado", "job", job.ID, "pr", args.PullRequestID,
		"parent", args.ParentCommentID, "review_encolada", res.EnqueueReview)
	return nil
}

// ---------------------------------------------------------------------------
// CleanupJobWorker — retención diaria (§9.11)
// ---------------------------------------------------------------------------

// CleanupJobWorker aplica la retención del sistema: webhook_deliveries más
// viejas que CLEANUP_RETENTION_DAYS, sesiones expiradas y workdirs huérfanos
// del root. Registra lo que limpió — nunca un recorte silencioso.
type CleanupJobWorker struct {
	river.WorkerDefaults[CleanupJobArgs]

	Store         *store.Store
	RetentionDays int // CLEANUP_RETENTION_DAYS (default 30)

	WorkdirRoot string // default /var/tmp (los tests usan un tmp propio)
}

func (w *CleanupJobWorker) register(b *river.Workers) error { return river.AddWorkerSafely(b, w) }

// Work ejecuta la limpieza. Cada paso es independiente: un fallo no tapa al
// siguiente — el job reintenta y lo ya limpiado es idempotente.
func (w *CleanupJobWorker) Work(ctx context.Context, _ *river.Job[CleanupJobArgs]) error {
	retention := w.RetentionDays
	if retention < 1 {
		retention = 30
	}
	cutoff := pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -retention), Valid: true}

	deliveries, err := w.Store.DeleteWebhookDeliveriesOlderThan(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("limpiando webhook_deliveries: %w", err)
	}
	sessions, err := w.Store.DeleteExpiredSessions(ctx)
	if err != nil {
		return fmt.Errorf("limpiando sesiones expiradas: %w", err)
	}
	workdirs := w.cleanWorkdirs()

	slog.Info("cleanup job: retención aplicada (§9.11)",
		"webhook_deliveries", deliveries, "sessions", sessions, "workdirs", workdirs,
		"retention_days", retention)
	return nil
}

// cleanWorkdirs borra los workdirs huérfanos del root: entries rev-* con
// mtime más viejo que workdirOrphanAge. Los recientes quedan — puede haber
// reviews en curso.
func (w *CleanupJobWorker) cleanWorkdirs() int {
	entries, err := filepath.Glob(filepath.Join(workdirRoot(w.WorkdirRoot), workdirPrefix+"*"))
	if err != nil {
		slog.Warn("cleanup job: listando workdirs", "error", err)
		return 0
	}
	removed := 0
	for _, e := range entries {
		info, err := os.Stat(e)
		if err != nil {
			continue // desapareció entre el glob y el stat: otro lo manejó
		}
		if time.Since(info.ModTime()) <= workdirOrphanAge {
			continue
		}
		if err := os.RemoveAll(e); err != nil {
			slog.Warn("cleanup job: no se pudo borrar el workdir huérfano", "path", e, "error", err)
			continue
		}
		removed++
	}
	return removed
}

// ---------------------------------------------------------------------------
// RotationJobWorker — re-cifrado al rotar la master key (§9.2)
// ---------------------------------------------------------------------------

// RotationJobWorker re-cifra las columnas cifradas con la clave nueva:
// llm_providers.api_key y los secretos por repo (webhook_secret, secret_token,
// api_token, deploy_key). Transición dual §9.2: descifra con MASTER_KEY
// (nueva) y, si falla, con MASTER_KEY_PREVIOUS (vieja); rehúsa arrancar sin
// la previa — re-cifrar lo que no se puede descifrar no es rotación. Cuando
// termina, la API retira _PREVIOUS del env.
type RotationJobWorker struct {
	river.WorkerDefaults[RotationJobArgs]

	Store  *store.Store
	NewKey []byte // MASTER_KEY (nueva): cifra la salida
	OldKey []byte // MASTER_KEY_PREVIOUS (vieja): fallback de descifrado
}

func (w *RotationJobWorker) register(b *river.Workers) error { return river.AddWorkerSafely(b, w) }

// Work re-cifra tabla por tabla. Un valor indescifrable aborta el job con
// error: se re-cifra a medias antes que corromper — lo ya re-cifrado queda
// bajo la clave nueva y el reintento es idempotente.
func (w *RotationJobWorker) Work(ctx context.Context, _ *river.Job[RotationJobArgs]) error {
	if len(w.NewKey) != 32 || len(w.OldKey) != 32 {
		return errors.New(
			"rotación de master key sin claves completas: exige MASTER_KEY + MASTER_KEY_PREVIOUS " +
				"(re-cifrar lo que no se puede descifrar no es rotación, §9.2)")
	}

	providers, err := w.Store.ListLlmProviders(ctx)
	if err != nil {
		return fmt.Errorf("listando proveedores LLM: %w", err)
	}
	rotatedProviders := 0
	for _, p := range providers {
		rotated, err := rotateText(w.NewKey, w.OldKey, p.ApiKey)
		if err != nil {
			return fmt.Errorf("llm_providers %d: %w", p.ID, err)
		}
		if rotated == nil {
			continue // ya estaba bajo la clave nueva
		}
		if _, err := w.Store.UpdateLlmProvider(ctx, store.UpdateLlmProviderParams{
			ID: p.ID, BaseUrl: p.BaseUrl, Model: p.Model,
			ApiKey: *rotated, Role: p.Role, Priority: p.Priority, Enabled: p.Enabled,
		}); err != nil {
			return fmt.Errorf("actualizando el proveedor %d: %w", p.ID, err)
		}
		rotatedProviders++
	}

	repos, err := w.Store.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("listando repositorios: %w", err)
	}
	rotatedRepos := 0
	for _, r := range repos {
		changed := false
		cols := []struct {
			name string
			val  *pgtype.Text
		}{
			{"webhook_secret", &r.WebhookSecret},
			{"secret_token", &r.SecretToken},
			{"api_token", &r.ApiToken},
			{"deploy_key", &r.DeployKey},
		}
		for _, c := range cols {
			rotated, err := rotatePgText(w.NewKey, w.OldKey, *c.val)
			if err != nil {
				return fmt.Errorf("repositories %d (%s): %w", r.ID, c.name, err)
			}
			if !rotated.Valid {
				continue
			}
			*c.val = rotated
			changed = true
		}
		if !changed {
			continue
		}
		if _, err := w.Store.UpdateRepository(ctx, store.UpdateRepositoryParams{
			ID: r.ID, Owner: r.Owner, Name: r.Name,
			WebhookSecret: r.WebhookSecret, SecretToken: r.SecretToken,
			ApiToken: r.ApiToken, BaseUrl: r.BaseUrl, DeployKey: r.DeployKey,
			ReviewDrafts: r.ReviewDrafts, Language: r.Language, ChatOrgOnly: r.ChatOrgOnly,
		}); err != nil {
			return fmt.Errorf("actualizando el repo %d: %w", r.ID, err)
		}
		rotatedRepos++
	}

	slog.Info("rotación de master key completa (§9.2)",
		"llm_providers", rotatedProviders, "repositories", rotatedRepos)
	return nil
}

// rotateText descifra con la clave nueva y, si falla, con la previa
// (transición dual §9.2) y re-cifra con la nueva. Devuelve nil si el valor
// ya está bajo la clave nueva — reintento idempotente.
func rotateText(newKey, oldKey []byte, ciphertext string) (*string, error) {
	if ciphertext == "" {
		return nil, nil
	}
	if _, err := store.Decrypt(newKey, ciphertext); err == nil {
		return nil, nil
	}
	plain, err := store.Decrypt(oldKey, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("no se pudo descifrar con ninguna de las dos claves: %w", err)
	}
	re, err := store.Encrypt(newKey, plain)
	if err != nil {
		return nil, fmt.Errorf("re-cifrando: %w", err)
	}
	return &re, nil
}

// rotatePgText es rotateText para una columna nullable: (rotada, error).
// Devuelve la columna tal cual si no había nada que rotar.
func rotatePgText(newKey, oldKey []byte, col pgtype.Text) (pgtype.Text, error) {
	if !col.Valid {
		return col, nil
	}
	rotated, err := rotateText(newKey, oldKey, col.String)
	if err != nil || rotated == nil {
		return col, err
	}
	return pgtype.Text{String: *rotated, Valid: true}, nil
}

// ---------------------------------------------------------------------------
// ReconcileJobWorker — reconciliación al reconectar un repo (§3.5, sin LLM)
// ---------------------------------------------------------------------------

// ReconcileJobWorker reconcilia el estado local de PRs contra el VCS: trae
// los PRs abiertos del repo vía ListOpenPRs y marca cerrados los que ya no
// figuran — los cierres durante la desconexión no llegan como evento.
type ReconcileJobWorker struct {
	river.WorkerDefaults[ReconcileJobArgs]

	Store    *store.Store
	Provider vcs.VCSProvider
}

func (w *ReconcileJobWorker) register(b *river.Workers) error { return river.AddWorkerSafely(b, w) }

// Work reconcilia un repo. Sin LLM: comparación de números contra la lista
// del VCS y update de estado.
func (w *ReconcileJobWorker) Work(ctx context.Context, job *river.Job[ReconcileJobArgs]) error {
	repo, err := w.Store.GetRepository(ctx, job.Args.RepositoryID)
	if err != nil {
		return fmt.Errorf("cargando el repo %d: %w", job.Args.RepositoryID, err)
	}
	if repo.Vcs != "github" {
		// ponytail: F1 reconcilia solo GitHub — GitLab (F2) entra a un
		// map[vcs]provider acá.
		return fmt.Errorf("reconciliación de %s no soportada aún (llega en F2)", repo.Vcs)
	}

	open, err := w.Provider.ListOpenPRs(ctx, &repo)
	if err != nil {
		return fmt.Errorf("listando PRs abiertos del VCS: %w", err)
	}
	stillOpen := make(map[int64]bool, len(open))
	for _, p := range open {
		stillOpen[p.Number] = true
	}

	local, err := w.Store.ListOpenPullRequestsByRepo(ctx, repo.ID)
	if err != nil {
		return fmt.Errorf("listando PRs abiertos locales: %w", err)
	}
	closed := 0
	for _, pr := range local {
		if stillOpen[pr.Number] {
			continue
		}
		// merged_at queda null: la reconexión no sabe si hubo merge — la
		// fila igual sale del listado de abiertos (§3.5).
		if _, err := w.Store.UpdatePullRequestState(ctx, store.UpdatePullRequestStateParams{
			ID: pr.ID, State: "closed",
		}); err != nil {
			return fmt.Errorf("cerrando el PR %d: %w", pr.Number, err)
		}
		closed++
	}
	slog.Info("reconcile job: reconexión reconciliada", "repo", repo.ID, "cerrados", closed)
	return nil
}

// Satisfacción de interfaces en tiempo de compilación: el worker consume
// exactamente el contrato que declara.
var (
	_ river.Worker[ReviewJobArgs]    = (*ReviewJobWorker)(nil)
	_ river.Worker[ChatJobArgs]      = (*ChatJobWorker)(nil)
	_ river.Worker[CleanupJobArgs]   = (*CleanupJobWorker)(nil)
	_ river.Worker[RotationJobArgs]  = (*RotationJobWorker)(nil)
	_ river.Worker[ReconcileJobArgs] = (*ReconcileJobWorker)(nil)
	_ Worker                         = (*ReviewJobWorker)(nil)
	_ Worker                         = (*ChatJobWorker)(nil)
	_ Worker                         = (*CleanupJobWorker)(nil)
	_ Worker                         = (*RotationJobWorker)(nil)
	_ Worker                         = (*ReconcileJobWorker)(nil)
	_ review.Gateway                 = (*llm.Gateway)(nil)
	_ review.Analyzer                = (*analyze.Runner)(nil)
)
