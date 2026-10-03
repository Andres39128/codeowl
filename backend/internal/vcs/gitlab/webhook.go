package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Cabeceras de entrega de GitLab (§9.3): Standard Webhooks (signing token) y
// el token legacy; el delivery ID de la dedup es webhook-id o, en su ausencia
// (self-managed < 17.4), Idempotency-Key — sin ambos no hay dedup.
const (
	headerWebhookID        = "Webhook-Id"
	headerWebhookTimestamp = "Webhook-Timestamp"
	headerWebhookSignature = "Webhook-Signature"
	headerLegacyToken      = "X-Gitlab-Token"
	headerIdempotencyKey   = "Idempotency-Key"
)

// Tipos del payload de webhook de GitLab: solo los campos que el filtro y el
// upsert consumen (§4.1: nada de modelar el payload completo). La clave
// object_attributes cambia de forma según object_kind (MR en merge_request,
// nota en note): viaja cruda y se decodea por rama.
type webhookPayload struct {
	ObjectKind       string          `json:"object_kind"`
	Project          glProject       `json:"project"`
	User             glUser          `json:"user"`
	ObjectAttributes json.RawMessage `json:"object_attributes"`
	Changes          *glChanges      `json:"changes"`
	MergeRequest     *glMRRef        `json:"merge_request"` // presente en note sobre MR
}

type glProject struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type glUser struct {
	Username string `json:"username"`
}

// glMRAttrs son los object_attributes de un evento merge_request (§3.5).
type glMRAttrs struct {
	Action       string    `json:"action"` // open/update/reopen/close/merge
	IID          int64     `json:"iid"`
	Draft        bool      `json:"draft"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	OldRev       string    `json:"oldrev"` // presente solo en update por push
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastCommit   *glCommit `json:"last_commit"`
}

type glCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// glChanges son los cambios del update: el filtro de §3.5 solo pasa push
// (oldrev), retarget (target_branch) o la transición draft→ready (draft con
// current=false). Título, labels y descripción no figuran acá — se descartan.
type glChanges struct {
	TargetBranch *struct{}      `json:"target_branch"` // presente ⇔ retarget
	Draft        *glDraftChange `json:"draft"`         // presente ⇔ transición de draft
}

type glDraftChange struct {
	Previous bool `json:"previous"`
	Current  bool `json:"current"`
}

// glNoteAttrs son los object_attributes de un evento note (§3.5).
type glNoteAttrs struct {
	NoteableType string `json:"noteable_type"` // solo "MergeRequest" pasa
	Note         string `json:"note"`
}

type glMRRef struct {
	IID int64 `json:"iid"`
}

// HandleWebhook es el handler de POST /webhooks/gitlab (§3.5/§9.3). El orden
// de costo de §9.3 se invierte en un punto, por diseño: el secret de la firma
// es por repo (columna webhook_secret del signing token), así que el payload
// se parsea para extraer el project ID, se resuelve el repo y recién entonces
// se verifica — tope de tamaño → parseo → repo → firma → dedup → filtro.
// Nada toca la cola antes de pasar la firma. Repo ausente o deshabilitado:
// 2xx y descarte con log, sin job ni fila de delivery (§3.5). Ante error de
// BD responde 5xx para que GitLab re-entregue.
func (a *Adapter) HandleWebhook(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	body, ok := a.readBody(w, r)
	if !ok {
		return
	}

	var p webhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		fail(ctx, w, http.StatusBadRequest, "payload JSON inválido", "err", err)
		return
	}

	repo, err := a.st.GetRepositoryByVCSExternalID(ctx, store.GetRepositoryByVCSExternalIDParams{
		Vcs: "gitlab", ExternalID: p.Project.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Repo no conectado: descarte con log, sin job ni fila de delivery.
		slog.InfoContext(ctx, "webhook gitlab: repo no conectado, descartado",
			"event", p.ObjectKind, "external_id", p.Project.ID)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		fail(ctx, w, http.StatusInternalServerError, "resolviendo repo del webhook", "external_id", p.Project.ID, "err", err)
		return
	}
	if !repo.Enabled {
		// Desconectado: mismo tratamiento que un repo ausente (§3.5).
		slog.InfoContext(ctx, "webhook gitlab: repo deshabilitado, descartado",
			"event", p.ObjectKind, "repo", repo.Owner+"/"+repo.Name)
		w.WriteHeader(http.StatusOK)
		return
	}

	if !a.verify(ctx, repo, r, body) {
		fail(ctx, w, http.StatusUnauthorized, "firma de webhook inválida",
			"event", p.ObjectKind, "repo", repo.Owner+"/"+repo.Name)
		return
	}

	delivery := r.Header.Get(headerWebhookID)
	if delivery == "" {
		delivery = r.Header.Get(headerIdempotencyKey) // 17.4+ sin signing token
	}

	// Dedup fast-path (§9.3): la re-entrega ya procesada corta acá. Sin
	// delivery ID (pre-17.4) no hay dedup: el upsert idempotente del PR y la
	// unicidad del ReviewJob acotan la re-entrega (§9.3).
	if delivery != "" {
		exists, err := a.st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{
			Vcs: "gitlab", DeliveryID: delivery,
		})
		if err != nil {
			fail(ctx, w, http.StatusInternalServerError, "consultando dedup de delivery", "delivery", delivery, "err", err)
			return
		}
		if exists {
			slog.InfoContext(ctx, "webhook gitlab: delivery duplicada, descartada",
				"event", p.ObjectKind, "delivery", delivery)
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Filtro de eventos suscritos (§3.5): cualquier otro evento se descarta
	// temprano, sin tocar la cola ni la BD.
	if !filterEvent(&p) {
		slog.InfoContext(ctx, "webhook gitlab: evento fuera del filtro, descartado",
			"event", p.ObjectKind, "delivery", delivery)
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := a.dispatch(ctx, repo, &p); err != nil {
		// Upsert/enqueue fallido (p. ej. BD caída): 5xx para re-entrega.
		fail(ctx, w, http.StatusInternalServerError, "procesando evento",
			"event", p.ObjectKind, "delivery", delivery, "err", err)
		return
	}

	// La delivery se registra al final: una falla de procesamiento queda sin
	// fila y la re-entrega repite el trabajo (idempotente). El duplicado en
	// vuelo que corrió la ventana del fast-path cae en el ON CONFLICT.
	if delivery != "" {
		if _, err := a.st.CreateWebhookDelivery(ctx, store.CreateWebhookDeliveryParams{
			Vcs: "gitlab", DeliveryID: delivery,
		}); errors.Is(err, pgx.ErrNoRows) {
			slog.InfoContext(ctx, "webhook gitlab: delivery duplicada en vuelo, descartada",
				"event", p.ObjectKind, "delivery", delivery)
		} else if err != nil {
			fail(ctx, w, http.StatusInternalServerError, "registrando delivery", "delivery", delivery, "err", err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// verify aplica §9.3: firma Standard Webhooks contra el signing token del
// repo, o fallback legacy X-Gitlab-Token contra el secret token. Sin ninguna
// de las dos cabeceras se rechaza: aceptar sin firma sería aceptar spoofing.
func (a *Adapter) verify(ctx context.Context, repo store.Repository, r *http.Request, body []byte) bool {
	if sig := r.Header.Get(headerWebhookSignature); sig != "" {
		secret, err := a.decryptSecret(repo.WebhookSecret, "webhook_secret (signing token)")
		if err != nil {
			slog.ErrorContext(ctx, "webhook gitlab: firma presente pero el repo no tiene signing token utilizable", "err", err)
			return false
		}
		if err := verifyStandardWebhooks(secret,
			r.Header.Get(headerWebhookID),
			r.Header.Get(headerWebhookTimestamp),
			sig, body, a.tolerance); err != nil {
			slog.InfoContext(ctx, "webhook gitlab: firma Standard Webhooks inválida", "err", err)
			return false
		}
		return true
	}
	if legacy := r.Header.Get(headerLegacyToken); legacy != "" {
		secret, err := a.decryptSecret(repo.SecretToken, "secret_token (legacy)")
		if err != nil {
			slog.ErrorContext(ctx, "webhook gitlab: X-Gitlab-Token presente pero el repo no tiene secret token", "err", err)
			return false
		}
		if !verifyLegacyToken(secret, legacy) {
			slog.InfoContext(ctx, "webhook gitlab: token legacy inválido")
			return false
		}
		return true
	}
	slog.InfoContext(ctx, "webhook gitlab: entrega sin firma ni token, rechazada")
	return false
}

// filterEvent aplica el filtro de eventos suscritos (§3.5): merge_request en
// open/reopen/close/merge; update solo si cambió el source (push, oldrev), la
// rama base (retarget) o la marca draft (solo draft→ready — la vuelta a draft
// se descarta, mismo tratamiento que converted_to_draft en GitHub; título,
// labels y descripción también). note pasa cruda: handleNote filtra
// noteable_type, mención y anti-bucle.
func filterEvent(p *webhookPayload) bool {
	switch p.ObjectKind {
	case "merge_request":
		if p.ObjectAttributes == nil {
			return false
		}
		var attrs glMRAttrs
		if err := json.Unmarshal(p.ObjectAttributes, &attrs); err != nil {
			return false
		}
		switch attrs.Action {
		case "open", "reopen", "close", "merge":
			return true
		case "update":
			if attrs.OldRev != "" {
				return true // push: nueva cabeza (§3.5)
			}
			if p.Changes != nil && p.Changes.TargetBranch != nil {
				return true // retarget: la review contra la base vieja quedó stale
			}
			if p.Changes != nil && p.Changes.Draft != nil && !p.Changes.Draft.Current {
				return true // draft → ready (equivalente a ready_for_review)
			}
			return false
		}
	case "note":
		return p.ObjectAttributes != nil
	}
	return false
}

// dispatch enruta el evento ya filtrado a su handler.
func (a *Adapter) dispatch(ctx context.Context, repo store.Repository, p *webhookPayload) error {
	switch p.ObjectKind {
	case "merge_request":
		return a.handleMergeRequest(ctx, repo, p)
	case "note":
		return a.handleNote(ctx, repo, p)
	}
	return nil
}

// handleMergeRequest procesa merge_request (§3.5): open/reopen y el update
// que pasó el filtro upsertan el PR y encolan el ReviewJob — resolviendo el
// tip de la rama base por API, porque el payload no trae SHA de la base
// (§3.6.1.1). close/merge actualizan estado y NO encolan (MetricsJob es F5);
// el descarte de jobs pendientes del PR lo hace el worker.
func (a *Adapter) handleMergeRequest(ctx context.Context, repo store.Repository, p *webhookPayload) error {
	var attrs glMRAttrs
	if err := json.Unmarshal(p.ObjectAttributes, &attrs); err != nil {
		return fmt.Errorf("decodificando object_attributes: %w", err)
	}

	switch attrs.Action {
	case "close", "merge":
		pr, err := a.st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
			RepositoryID: repo.ID, Number: attrs.IID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			slog.InfoContext(ctx, "webhook gitlab: cierre de MR no observado, descartado", "mr", attrs.IID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("resolviendo MR %d: %w", attrs.IID, err)
		}
		// El payload de GitLab no trae merged_at: el momento del merge es el
		// updated_at del evento; en close queda null (§3.5).
		var mergedAt pgtype.Timestamptz
		if attrs.Action == "merge" && !attrs.UpdatedAt.IsZero() {
			mergedAt = pgtype.Timestamptz{Time: attrs.UpdatedAt, Valid: true}
		}
		if _, err := a.st.UpdatePullRequestState(ctx, store.UpdatePullRequestStateParams{
			ID: pr.ID, State: "closed", MergedAt: mergedAt,
		}); err != nil {
			return fmt.Errorf("cerrando el MR %d: %w", attrs.IID, err)
		}
		slog.InfoContext(ctx, "webhook gitlab: MR cerrado", "mr", attrs.IID, "merged", attrs.Action == "merge")
		return nil
	}

	// open/reopen/update: nueva identidad de corrida.
	headSHA := ""
	if attrs.LastCommit != nil {
		headSHA = attrs.LastCommit.ID
	}
	if headSHA == "" {
		return fmt.Errorf("el MR %d no informa last_commit.id", attrs.IID)
	}
	baseSHA, err := a.branchTip(ctx, &repo, attrs.TargetBranch)
	if err != nil {
		return fmt.Errorf("resolviendo la base del MR %d: %w", attrs.IID, err)
	}

	// El payload no trae al autor del MR (user es el actor del evento): en
	// update se conserva el autor de la fila — el actor puede ser un
	// mantenedor que pusheó.
	author := p.User.Username
	if attrs.Action == "update" {
		if existing, err := a.st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
			RepositoryID: repo.ID, Number: attrs.IID,
		}); err == nil {
			author = existing.Author
		}
	}

	row, err := a.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID,
		Number:       attrs.IID,
		Author:       author,
		State:        "open",
		HeadSha:      headSHA,
		BaseRef:      attrs.TargetBranch,
		BaseSha:      baseSHA,
		CreatedAt:    pgtype.Timestamptz{Time: attrs.CreatedAt, Valid: !attrs.CreatedAt.IsZero()},
	})
	if err != nil {
		return fmt.Errorf("upsert del MR %d: %w", attrs.IID, err)
	}

	// Draft: no se revisa por default — config por repo (§3.5). El upsert de
	// arriba igual deja la fila al día para el chat de F2.
	if attrs.Draft && !repo.ReviewDrafts {
		slog.InfoContext(ctx, "webhook gitlab: MR draft sin review (config del repo)",
			"mr", attrs.IID, "action", attrs.Action)
		return nil
	}

	args, err := json.Marshal(jobs.ReviewJobArgs{
		RepositoryID:  repo.ID,
		PullRequestID: row.ID,
		HeadSha:       headSHA,
		BaseSha:       baseSHA,
	})
	if err != nil {
		return fmt.Errorf("serializando args del ReviewJob: %w", err)
	}
	if err := a.jq.Enqueue(ctx, jobs.KindReview, args); err != nil {
		return fmt.Errorf("encolando ReviewJob del MR %d: %w", attrs.IID, err)
	}
	return nil
}

// handleNote filtra la nota (§3.5): solo noteable_type == MergeRequest, con
// mención al bot y sin comentarios del propio bot (anti-bucle — GitLab no
// expone flag de autor bot: comparación contra el username configurado), y el
// MR debe existir. El ChatJob llega en F2: por ahora el comentario que pasa
// el filtro se registra y descarta, sin job.
func (a *Adapter) handleNote(ctx context.Context, repo store.Repository, p *webhookPayload) error {
	var attrs glNoteAttrs
	if err := json.Unmarshal(p.ObjectAttributes, &attrs); err != nil {
		return fmt.Errorf("decodificando la nota: %w", err)
	}
	if attrs.NoteableType != "MergeRequest" {
		slog.InfoContext(ctx, "webhook gitlab: nota fuera de un MR, descartada", "noteable_type", attrs.NoteableType)
		return nil
	}
	if p.MergeRequest == nil {
		slog.InfoContext(ctx, "webhook gitlab: nota de MR sin objeto merge_request, descartada")
		return nil
	}
	// Anti-bucle (§3.5): responderse a sí mismo es el loop clásico con
	// presupuesto LLM quemado.
	if p.User.Username == a.botUsername {
		slog.InfoContext(ctx, "webhook gitlab: nota del propio bot ignorada (anti-bucle)", "author", p.User.Username)
		return nil
	}
	if !strings.Contains(attrs.Note, "@"+a.botUsername) {
		slog.InfoContext(ctx, "webhook gitlab: nota sin mención al bot, descartada", "mr", p.MergeRequest.IID)
		return nil
	}
	if _, err := a.st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: p.MergeRequest.IID,
	}); errors.Is(err, pgx.ErrNoRows) {
		slog.InfoContext(ctx, "webhook gitlab: nota sobre MR no observado, descartada", "mr", p.MergeRequest.IID)
		return nil
	} else if err != nil {
		return fmt.Errorf("resolviendo MR %d de la nota: %w", p.MergeRequest.IID, err)
	}
	slog.InfoContext(ctx, "webhook gitlab: mención en MR (ChatJob llega en F2), descartada", "mr", p.MergeRequest.IID)
	return nil
}

// readBody lee el body con el tope de tamaño (§9.3: se rechaza antes de
// parsear). Devuelve ok=false si ya escribió la respuesta.
func (a *Adapter) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, a.maxBody))
	if err == nil {
		return body, true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		fail(context.Background(), w, http.StatusRequestEntityTooLarge, "payload sobre el tope", "tope", a.maxBody)
		return nil, false
	}
	fail(context.Background(), w, http.StatusBadRequest, "body ilegible", "err", err)
	return nil, false
}

// fail escribe el status y registra el fallo (log estructurado §9.9). Los
// 5xx piden re-entrega al VCS; la re-entrega repite el trabajo de forma
// idempotente.
func fail(ctx context.Context, w http.ResponseWriter, status int, msg string, args ...any) {
	slog.ErrorContext(ctx, "webhook gitlab: "+msg, args...)
	w.WriteHeader(status)
}
