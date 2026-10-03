package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Cabeceras de entrega de GitHub (§9.3): evento, firma y delivery GUID.
const (
	headerEvent     = "X-GitHub-Event"
	headerSignature = "X-Hub-Signature-256"
	headerDelivery  = "X-GitHub-Delivery"
	signaturePrefix = "sha256="
)

// Tipos del payload de webhook de GitHub: solo los campos que el filtro y
// el upsert consumen (§4.1: nada de modelar el payload completo).
type webhookPayload struct {
	Action      string         `json:"action"`
	PullRequest *ghPullRequest `json:"pull_request"`
	Issue       *ghIssue       `json:"issue"`
	Comment     *ghComment     `json:"comment"`
	Changes     *ghChanges     `json:"changes"`
	Repository  ghRepository   `json:"repository"`
}

type ghPullRequest struct {
	Number    int64      `json:"number"`
	State     string     `json:"state"`
	Draft     bool       `json:"draft"`
	MergedAt  *time.Time `json:"merged_at"` // null salvo merge
	CreatedAt time.Time  `json:"created_at"`
	User      ghActor    `json:"user"`
	Head      ghRef      `json:"head"`
	Base      ghRef      `json:"base"`
}

type ghRef struct {
	Ref string `json:"ref"`
	Sha string `json:"sha"`
}

type ghIssue struct {
	Number int64 `json:"number"`
	// PullRequest está presente en el payload si y solo si el issue es un PR
	// (§3.5: issue_comment llega por issues y PRs por igual).
	PullRequest *struct{} `json:"pull_request"`
}

type ghComment struct {
	ID   int64   `json:"id"`
	Body string  `json:"body"`
	User ghActor `json:"user"`
	// AuthorAssociation relation del autor con el repo (MEMBER, OWNER,
	// CONTRIBUTOR, NONE...): filtro chat_org_only (§3.5).
	AuthorAssociation string `json:"author_association"`
}

type ghActor struct {
	Login string `json:"login"`
	Type  string `json:"type"` // "Bot" para Apps y bots — anti-bucle (§3.5)
}

type ghChanges struct {
	// Base presente ⇔ el edit cambió la base del PR: retarget (§3.5).
	Base *struct{} `json:"base"`
}

type ghRepository struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// HandleWebhook es el handler de POST /webhooks/github (§3.5/§9.3). Procesa
// en orden de costo — tope de tamaño → firma → dedup por delivery ID →
// parseo → filtro de evento — y nada toca la cola ni la BD antes de pasar la
// firma. Respuesta inmediata, cero trabajo: valida, filtra, upserta y
// encola — jamás llama LLM ni ejecuta análisis. Ante error de BD responde
// 5xx para que GitHub re-entregue: la dedup por delivery ID la hace segura.
func (a *Adapter) HandleWebhook(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	body, ok := a.readBody(w, r)
	if !ok {
		return
	}

	// La firma primero (§9.3: nada toca la cola ni la BD antes de pasarla,
	// ni siquiera la lectura de cabeceras de entrega).
	if !a.verifySignature(body, r.Header.Get(headerSignature)) {
		fail(ctx, w, http.StatusUnauthorized, "firma HMAC inválida",
			"event", r.Header.Get(headerEvent), "delivery", r.Header.Get(headerDelivery))
		return
	}

	event := r.Header.Get(headerEvent)
	delivery := r.Header.Get(headerDelivery)
	if event == "" || delivery == "" {
		fail(ctx, w, http.StatusBadRequest, "entrega sin cabeceras de evento o delivery")
		return
	}

	// Dedup fast-path (§9.3): la re-entrega ya procesada corta acá. El
	// insert autoritativo va después de resolver el repo — un repo ausente
	// o deshabilitado no genera estado (§3.5: solo repos conectados).
	exists, err := a.st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{
		Vcs: "github", DeliveryID: delivery,
	})
	if err != nil {
		fail(ctx, w, http.StatusInternalServerError, "consultando dedup de delivery", "delivery", delivery, "err", err)
		return
	}
	if exists {
		slog.InfoContext(ctx, "webhook github: delivery duplicada, descartada",
			"event", event, "delivery", delivery)
		w.WriteHeader(http.StatusOK)
		return
	}

	var p webhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		fail(ctx, w, http.StatusBadRequest, "payload JSON inválido con firma válida", "delivery", delivery, "err", err)
		return
	}

	// Filtro de eventos suscritos (§3.5): cualquier otro evento o acción se
	// descarta temprano, sin tocar la cola ni la BD.
	if !filterEvent(event, &p) {
		slog.InfoContext(ctx, "webhook github: evento fuera del filtro, descartado",
			"event", event, "action", p.Action, "delivery", delivery)
		w.WriteHeader(http.StatusOK)
		return
	}

	repo, err := a.st.GetRepositoryByVCSExternalID(ctx, store.GetRepositoryByVCSExternalIDParams{
		Vcs: "github", ExternalID: p.Repository.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Repo no conectado: descarte con log, sin job ni fila de delivery (§3.5).
		slog.InfoContext(ctx, "webhook github: repo no conectado, descartado",
			"event", event, "external_id", p.Repository.ID, "delivery", delivery)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		fail(ctx, w, http.StatusInternalServerError, "resolviendo repo del webhook", "external_id", p.Repository.ID, "err", err)
		return
	}
	if !repo.Enabled {
		// Desconectado: mismo tratamiento que un repo ausente (§3.5).
		slog.InfoContext(ctx, "webhook github: repo deshabilitado, descartado",
			"event", event, "repo", repo.Owner+"/"+repo.Name, "delivery", delivery)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Dedup autoritativa: la primera entrega inserta; un duplicado que
	// corrió la ventana del fast-path (misma entrega en vuelo) cae acá.
	if _, err := a.st.CreateWebhookDelivery(ctx, store.CreateWebhookDeliveryParams{
		Vcs: "github", DeliveryID: delivery,
	}); errors.Is(err, pgx.ErrNoRows) {
		slog.InfoContext(ctx, "webhook github: delivery duplicada en vuelo, descartada",
			"event", event, "delivery", delivery)
		w.WriteHeader(http.StatusOK)
		return
	} else if err != nil {
		fail(ctx, w, http.StatusInternalServerError, "registrando delivery", "delivery", delivery, "err", err)
		return
	}

	switch event {
	case "pull_request":
		err = a.handlePullRequest(ctx, repo, &p)
	case "issue_comment":
		err = a.handleIssueComment(ctx, repo, &p)
	case "pull_request_review_comment":
		err = a.handleReviewComment(ctx, repo, &p)
	}
	if err != nil {
		// Upsert/enqueue fallido (p. ej. BD caída): 5xx para re-entrega —
		// la dedup la hace segura (§3.5).
		fail(ctx, w, http.StatusInternalServerError, "procesando evento", "event", event, "action", p.Action, "delivery", delivery, "err", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// filterEvent aplica el filtro de eventos suscritos (§3.5): pull_request en
// opened/synchronize/reopened/ready_for_review, edited solo por retarget
// (changes.base), closed; comentarios created en la conversación o en un
// hilo inline. Todo lo demás devuelve false.
func filterEvent(event string, p *webhookPayload) bool {
	switch event {
	case "pull_request":
		if p.PullRequest == nil {
			return false
		}
		switch p.Action {
		case "opened", "synchronize", "reopened", "ready_for_review", "closed":
			return true
		case "edited":
			return p.Changes != nil && p.Changes.Base != nil
		}
		return false // converted_to_draft, labeled, assigned...: descarte
	case "issue_comment":
		return p.Action == "created" && p.Issue != nil && p.Comment != nil
	case "pull_request_review_comment":
		return p.Action == "created" && p.PullRequest != nil && p.Comment != nil
	}
	return false
}

// handlePullRequest upserta el PR (idempotente por repo+number) y encola el
// ReviewJob para las acciones de review. El cierre actualiza estado y NO
// encola: el MetricsJob llega en F5 (§3.5). converted_to_draft y el resto
// de acciones nunca llegan acá: las corta filterEvent.
func (a *Adapter) handlePullRequest(ctx context.Context, repo store.Repository, p *webhookPayload) error {
	pr := p.PullRequest
	mergedAt := pgtype.Timestamptz{}
	if pr.MergedAt != nil {
		mergedAt = pgtype.Timestamptz{Time: *pr.MergedAt, Valid: true}
	}
	row, err := a.st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
		RepositoryID: repo.ID,
		Number:       pr.Number,
		Author:       pr.User.Login,
		State:        pr.State,
		HeadSha:      pr.Head.Sha,
		BaseRef:      pr.Base.Ref,
		BaseSha:      pr.Base.Sha,
		MergedAt:     mergedAt,
		CreatedAt:    pgtype.Timestamptz{Time: pr.CreatedAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("upsert del PR %d: %w", pr.Number, err)
	}

	if p.Action == "closed" {
		// Cierre: solo estado — sin job (§3.5). El descarte de jobs
		// pendientes del PR lo hace el worker desde su tarea de jobs.
		slog.InfoContext(ctx, "webhook github: PR cerrado", "pr", pr.Number, "merged", pr.MergedAt != nil)
		return nil
	}

	// Draft: no se revisa por default — config por repo (§3.5). El upsert
	// de arriba igual deja la fila al día para el chat de F2.
	if pr.Draft && !repo.ReviewDrafts {
		slog.InfoContext(ctx, "webhook github: PR draft sin review (config del repo)",
			"pr", pr.Number, "action", p.Action)
		return nil
	}

	args, err := json.Marshal(jobs.ReviewJobArgs{
		RepositoryID:  repo.ID,
		PullRequestID: row.ID,
		HeadSha:       pr.Head.Sha,
		BaseSha:       pr.Base.Sha,
	})
	if err != nil {
		return fmt.Errorf("serializando args del ReviewJob: %w", err)
	}
	if err := a.jq.Enqueue(ctx, jobs.KindReview, args); err != nil {
		return fmt.Errorf("encolando ReviewJob del PR %d: %w", pr.Number, err)
	}
	return nil
}

// handleIssueComment procesa el comentario de la conversación (§3.5): sin
// bots (anti-bucle) y solo PRs; los filtros comunes del chat y el encolado
// del ChatJob viven en enqueueChat (F2).
func (a *Adapter) handleIssueComment(ctx context.Context, repo store.Repository, p *webhookPayload) error {
	if p.Comment.User.Type == "Bot" {
		slog.InfoContext(ctx, "webhook github: comentario de bot ignorado (anti-bucle)",
			"author", p.Comment.User.Login)
		return nil
	}
	if p.Issue.PullRequest == nil {
		// Filtro ya lo habría cortado solo si faltara el campo; corte
		// explícito: es un issue, no un PR (§3.5).
		return nil
	}
	return a.enqueueChat(ctx, repo, p.Issue.Number, p.Comment)
}

// handleReviewComment procesa el comentario de hilo inline (§3.5): mismo
// tratamiento que issue_comment — ambos eventos alimentan el mismo Chat.
func (a *Adapter) handleReviewComment(ctx context.Context, repo store.Repository, p *webhookPayload) error {
	if p.Comment.User.Type == "Bot" {
		slog.InfoContext(ctx, "webhook github: comentario de bot ignorado (anti-bucle)",
			"author", p.Comment.User.Login)
		return nil
	}
	return a.enqueueChat(ctx, repo, p.PullRequest.Number, p.Comment)
}

// enqueueChat aplica los filtros comunes del chat (§3.5): mención al bot,
// PR observado y — si el repo lo exige (chat_org_only) — autor MEMBER/OWNER.
// Pasa → encola el ChatJob con la identidad actual del PR; el webhook jamás
// llama LLM (§3.5).
func (a *Adapter) enqueueChat(ctx context.Context, repo store.Repository, number int64, c *ghComment) error {
	if !strings.Contains(c.Body, "@"+a.botUsername) {
		slog.InfoContext(ctx, "webhook github: comentario sin mención al bot, descartado", "pr", number)
		return nil
	}
	pr, err := a.st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: number,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		slog.InfoContext(ctx, "webhook github: comentario sobre PR no observado, descartado", "pr", number)
		return nil
	} else if err != nil {
		return fmt.Errorf("resolviendo PR %d del comentario: %w", number, err)
	}
	if repo.ChatOrgOnly && !orgAuthorAssociation(c.AuthorAssociation) {
		slog.InfoContext(ctx, "webhook github: autor fuera de la organización (chat_org_only), descartado",
			"pr", number, "author_association", c.AuthorAssociation)
		return nil
	}
	args, err := json.Marshal(jobs.ChatJobArgs{
		RepositoryID:    repo.ID,
		PullRequestID:   pr.ID,
		ParentCommentID: strconv.FormatInt(c.ID, 10),
		CommentBody:     c.Body,
		CommentAuthor:   c.User.Login,
		HeadSha:         pr.HeadSha,
		BaseSha:         pr.BaseSha,
		Language:        repo.Language,
	})
	if err != nil {
		return fmt.Errorf("serializando args del ChatJob: %w", err)
	}
	if err := a.jq.Enqueue(ctx, jobs.KindChat, args); err != nil {
		return fmt.Errorf("encolando ChatJob del PR %d: %w", number, err)
	}
	return nil
}

// orgAuthorAssociation dice si la asociación del autor pasa el filtro
// chat_org_only (§3.5: pasan MEMBER/OWNER).
func orgAuthorAssociation(assoc string) bool {
	return assoc == "MEMBER" || assoc == "OWNER"
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

// verifySignature chequea X-Hub-Signature-256: HMAC-SHA256 sobre el body
// crudo, comparación en tiempo constante (§9.3). Sin secret configurado se
// rechaza toda entrega: aceptar sin firma sería aceptar spoofing.
func (a *Adapter) verifySignature(body []byte, header string) bool {
	secret := a.cfg.Stage2.GitHubWebhookSecret
	if secret == "" {
		slog.Error("webhook github: GITHUB_WEBHOOK_SECRET no configurado: se rechaza toda entrega")
		return false
	}
	sig, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), got)
}

// fail escribe el status y registra el fallo (log estructurado §9.9). Los
// 5xx piden re-entrega al VCS; la dedup por delivery ID la hace segura.
func fail(ctx context.Context, w http.ResponseWriter, status int, msg string, args ...any) {
	slog.ErrorContext(ctx, "webhook github: "+msg, args...)
	w.WriteHeader(status)
}
