//go:build integration

// Test de integración GitLab → Chat (mapa: backend.internal/integration — F2):
// webhook merge_request firmado (Standard Webhooks) → upsert + ReviewJob;
// webhook note con mención → ChatJob; ejecución de HandleChat contra el LLM
// stub → respuesta publicada por PostReply y registrada en comments_sent.
// Nada toca internet: la API de GitLab (branches) es un stub httptest al que
// apunta base_url, el LLM es un stub OpenAI-compatible y el VCS de publicación
// es un stub que graba. Requiere DATABASE_URL — sin ella el test se salta.
package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/api"
	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	gitlab "github.com/Andres39128/codeowl/backend/internal/vcs/gitlab"
)

// Credenciales del webhook GitLab de test: signing token con el formato
// Standard Webhooks (whsec_ + base64 de la clave cruda de 32 bytes, §9.3) y
// master key AES-256 que cifra las columnas secretas del repo.
var (
	glSigningKey   = []byte("codeowl-gitlab-signing-test-key-32b!")
	glSigningToken = "whsec_" + base64.StdEncoding.EncodeToString(glSigningKey)
	glMasterKey    = bytes.Repeat([]byte{0x51}, 32)
	glBotUsername  = "codeowl-bot"
)

// Identidades de la corrida: external_id fijo (repo GitLab), iid del MR,
// SHA de head realista (hex de 40) y las notas de chat (cada una con id
// propio: es la unidad de idempotencia parent_comment_id).
const (
	glProjectID = int64(999)
	glMRIID     = int64(42)
	glHeadSHA   = "aaaabbbbccccddddeeeeffff0000111122223333"
	glNoteChat  = int64(555)
	glNoteRev   = int64(556)
	glNoteRevX  = int64(557)
)

// glChatMarker y glChatReply enrutan la respuesta del stub LLM: el prompt de
// usuario del chat arranca con este marcador (review.chatUserMarker — no
// exportado, misma constante literal).
const (
	glChatMarker = "Comentario del usuario en el pull request:"
	glChatReply  = "Stub: el diff agrega el import fmt y cambia el saludo de main."
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// glChatStubLLM arma el servidor OpenAI-compatible del chat: responde en
// {base}/v1/chat/completions SOLO para prompts con el marcador del chat —
// cualquier otro prompt (agentes de review) es un 400 que hace fallar el test.
func glChatStubLLM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			http.Error(w, "request inválido", http.StatusBadRequest)
			return
		}
		if last := req.Messages[len(req.Messages)-1].Content; !strings.HasPrefix(last, glChatMarker) {
			http.Error(w, "prompt fuera del chat: agente inesperado", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":9,"completion_tokens":7}}`, glChatReply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// glReply es una publicación en hilo capturada: comentario padre + cuerpo.
type glReply struct {
	parent, body string
}

// chatStubVCS graba las respuestas de chat (PostReply) y sirve el diff al
// pipeline: contrato vcs.VCSProvider completo, sin HTTP real.
type chatStubVCS struct {
	diff string

	mu      sync.Mutex
	replies []glReply
}

var _ vcs.VCSProvider = (*chatStubVCS)(nil)

func (s *chatStubVCS) HandleWebhook(context.Context, http.ResponseWriter, *http.Request) {}
func (s *chatStubVCS) FetchPR(context.Context, *store.Repository, *store.PullRequest, string) error {
	return nil
}
func (s *chatStubVCS) FetchDefaultBranch(context.Context, *store.Repository, string) error {
	return nil
}
func (s *chatStubVCS) FetchPRTimeline(context.Context, *store.Repository, *store.PullRequest) (*vcs.PRTimeline, error) {
	return &vcs.PRTimeline{}, nil
}
func (s *chatStubVCS) ListOpenPRs(context.Context, *store.Repository) ([]vcs.OpenPR, error) {
	return nil, nil
}
func (s *chatStubVCS) GetDiff(context.Context, *store.Repository, *store.PullRequest) (string, error) {
	return s.diff, nil
}
func (s *chatStubVCS) PostSuggestion(context.Context, *store.Repository, *store.PullRequest, vcs.CommentPosition, string) (string, error) {
	return "", nil
}
func (s *chatStubVCS) PostInlineComment(context.Context, *store.Repository, *store.PullRequest, vcs.CommentPosition, string) (string, error) {
	return "", nil
}
func (s *chatStubVCS) PostSummary(context.Context, *store.Repository, *store.PullRequest, string, string) (string, error) {
	return "", nil
}
func (s *chatStubVCS) PostReply(_ context.Context, _ *store.Repository, _ *store.PullRequest, parent, body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, glReply{parent: parent, body: body})
	return fmt.Sprintf("reply-%d", len(s.replies)), nil
}

// snapshot copia las respuestas capturadas hasta ahora.
func (s *chatStubVCS) snapshot() []glReply {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]glReply(nil), s.replies...)
}

// ---------------------------------------------------------------------------
// Helpers de entorno
// ---------------------------------------------------------------------------

// newGitlabRepo conecta un repo GitLab efímero con signing token, api token y
// deploy key cifrados (§9.2), y base_url apuntando al stub de la API v4.
func newGitlabRepo(t *testing.T, st *store.Store, apiStubURL string) store.Repository {
	t.Helper()
	ctx := context.Background()
	enc := func(plain string) pgtype.Text {
		b, err := store.Encrypt(glMasterKey, []byte(plain))
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		return pgtype.Text{String: b, Valid: true}
	}
	repo, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs:           "gitlab",
		ExternalID:    glProjectID,
		Owner:         "codeowl-tests",
		Name:          fmt.Sprintf("gitlab-chat-%d", time.Now().UnixNano()),
		WebhookSecret: enc(glSigningToken),
		ApiToken:      enc("project-access-token-de-test"),
		DeployKey:     enc("ssh-ed25519 AAAA... clave-de-integracion"),
		BaseUrl:       pgtype.Text{String: apiStubURL, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	return repo
}

// glAPIStub sirve el endpoint de branches de la API v4 (§3.6.1.1): el tip de
// cada rama es determinístico ("tip-<rama>") — el adapter lo consulta para
// resolver el SHA de la base, que el payload de GitLab no trae.
func glAPIStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/")
		parts := strings.Split(rest, "/")
		if len(parts) == 4 && parts[1] == "repository" && parts[2] == "branches" {
			branch, err := url.PathUnescape(parts[3])
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":   branch,
				"commit": map[string]any{"id": "tip-" + branch},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// glMRPayload arma el payload de un evento merge_request (§3.5).
func glMRPayload(projectID, iid int64, action, head, target string) map[string]any {
	return map[string]any{
		"object_kind": "merge_request",
		"project":     map[string]any{"id": projectID, "name": "repo-integration"},
		"user":        map[string]any{"username": "dev-integration"},
		"object_attributes": map[string]any{
			"action":        action,
			"iid":           iid,
			"draft":         false,
			"source_branch": "feature/chat",
			"target_branch": target,
			"created_at":    "2026-01-15T10:00:00Z",
			"updated_at":    "2026-01-15T10:05:00Z",
			"last_commit":   map[string]any{"id": head, "message": "cambios del MR"},
		},
	}
}

// glNotePayload arma el payload de un evento note sobre un MR (§3.5).
func glNotePayload(projectID, noteID int64, body, author string, mrIID int64) map[string]any {
	return map[string]any{
		"object_kind": "note",
		"project":     map[string]any{"id": projectID, "name": "repo-integration"},
		"user":        map[string]any{"username": author},
		"object_attributes": map[string]any{
			"id":            noteID,
			"noteable_type": "MergeRequest",
			"note":          body,
		},
		"merge_request": map[string]any{"iid": mrIID},
	}
}

// postGitlabWebhook firma el payload con Standard Webhooks (§9.3: HMAC
// SHA-256 sobre "{webhook-id}.{webhook-timestamp}.{body}" con la clave cruda
// del signing token, firma "v1,{base64}") y lo manda por HTTP real a
// /webhooks/gitlab. Registra el webhook-id para la limpieza.
func postGitlabWebhook(t *testing.T, serverURL string, payload map[string]any, deliveries *[]string) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("serializando payload: %v", err)
	}
	whID := fmt.Sprintf("gl-integration-%d-%d", time.Now().UnixNano(), deliverySeq.Add(1))
	*deliveries = append(*deliveries, whID)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, glSigningKey)
	mac.Write([]byte(whID))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)

	req, err := http.NewRequest(http.MethodPost, serverURL+"/webhooks/gitlab", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("armado del POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Webhook-Id", whID)
	req.Header.Set("Webhook-Timestamp", ts)
	req.Header.Set("Webhook-Signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhooks/gitlab: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// cleanupGitlabRows borra las filas del test en orden hoja → raíz.
func cleanupGitlabRows(t *testing.T, st *store.Store, repoID, providerID int64, stubURL string, deliveries []string) {
	t.Helper()
	ctx := context.Background()
	for _, paso := range []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM llm_usage WHERE provider = $1", []any{stubURL}},
		{"DELETE FROM llm_providers WHERE id = $1", []any{providerID}},
		{"DELETE FROM comments_sent WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = $1)", []any{repoID}},
		{"DELETE FROM pull_requests WHERE repository_id = $1", []any{repoID}},
		{"DELETE FROM webhook_deliveries WHERE vcs = 'gitlab' AND delivery_id = ANY($1)", []any{deliveries}},
		{"DELETE FROM repositories WHERE id = $1", []any{repoID}},
	} {
		if _, err := st.Pool.Exec(ctx, paso.sql, paso.args...); err != nil {
			t.Errorf("limpieza: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Test E2E
// ---------------------------------------------------------------------------

// TestGitLabWebhookToChat recorre el flujo de chat de F2 de punta a punta:
// MR abierto firmado → upsert + ReviewJob; nota con mención → ChatJob;
// HandleChat (/explain) → respuesta en el hilo + comments_sent; /review en
// abierto → EnqueueReview; /review en cerrado → negativa sin encolar.
func TestGitLabWebhookToChat(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	// -- Setup: repo GitLab + stub de API + proveedor LLM + cadena completa --
	cfg := &config.Config{
		MasterKey: glMasterKey,
		Stage2:    config.Stage2Config{GitLabBotUsername: glBotUsername},
	}

	glAPI := glAPIStub(t)
	repo := newGitlabRepo(t, st, glAPI.URL)

	chatSrv := glChatStubLLM(t)
	encKey, err := store.Encrypt(glMasterKey, []byte("sk-stub-integration"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	provider, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl:  chatSrv.URL,
		Model:    "stub-model",
		ApiKey:   encKey,
		Role:     "review",
		Priority: 1,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateLlmProvider: %v", err)
	}

	// Cadena completa: cola capturadora → adapter GitLab → mux real de la API
	// (logging + recover + POST /webhooks/gitlab).
	q := newCapturedQueue()
	glAdapter := gitlab.New(st, cfg, q)
	apiSrv := httptest.NewServer(api.New(st, cfg, nil, glAdapter, q, nil).Routes())
	t.Cleanup(apiSrv.Close)

	gateway := llm.New(st, glMasterKey, llm.Limits{
		MaxPerReview: 2, MaxGlobal: 2, Timeout: 5 * time.Second, MaxRetries: 1,
	})
	vcsStub := &chatStubVCS{diff: diffFixture}

	var deliveries []string
	t.Cleanup(func() { cleanupGitlabRows(t, st, repo.ID, provider.ID, chatSrv.URL, deliveries) })

	// -- 1. Webhook merge_request open ---------------------------------------
	resp := postGitlabWebhook(t, apiSrv.URL, glMRPayload(glProjectID, glMRIID, "open", glHeadSHA, "main"), &deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("open: la API debe responder 2xx, fue %d", resp.StatusCode)
	}

	// Fila del MR upsertada: la base se resuelve por API al tip de la rama
	// target (§3.6.1.1) — "tip-main" en el stub.
	pr, err := st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: glMRIID,
	})
	if err != nil {
		t.Fatalf("el MR debe upsertearse con el webhook: %v", err)
	}
	if pr.Author != "dev-integration" || pr.State != "open" ||
		pr.HeadSha != glHeadSHA || pr.BaseRef != "main" || pr.BaseSha != "tip-main" {
		t.Errorf("fila del MR incorrecta: %+v", pr)
	}

	// Delivery registrada (dedup de re-entregas, §9.3) + ReviewJob encolado.
	exists, err := st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{
		Vcs: "gitlab", DeliveryID: deliveries[0],
	})
	if err != nil || !exists {
		t.Errorf("la delivery debe quedar registrada: exists=%v err=%v", exists, err)
	}
	jobs1 := q.pending()
	if len(jobs1) != 1 || jobs1[0].Kind != jobs.KindReview {
		t.Fatalf("open debe encolar 1 ReviewJob, encoló %+v", jobs1)
	}
	var revArgs jobs.ReviewJobArgs
	if err := json.Unmarshal(jobs1[0].Args, &revArgs); err != nil {
		t.Fatalf("args del ReviewJob: %v", err)
	}
	if revArgs.PullRequestID != pr.ID || revArgs.RepositoryID != repo.ID ||
		revArgs.HeadSha != glHeadSHA || revArgs.BaseSha != "tip-main" {
		t.Errorf("args del ReviewJob incorrectos: %+v", revArgs)
	}

	// -- 2. Webhook note con mención (disparador del chat) --------------------
	nota := "@" + glBotUsername + " /explain este cambio"
	resp = postGitlabWebhook(t, apiSrv.URL,
		glNotePayload(glProjectID, glNoteChat, nota, "dev-integration", glMRIID), &deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("note: la API debe responder 2xx, fue %d", resp.StatusCode)
	}

	jobs2 := q.pending() // pending() drena: el conteo es incremental por etapa
	if len(jobs2) != 1 || jobs2[0].Kind != jobs.KindChat {
		t.Fatalf("la mención debe encolar 1 ChatJob, encoló %+v", jobs2)
	}
	var chatArgs jobs.ChatJobArgs
	if err := json.Unmarshal(jobs2[0].Args, &chatArgs); err != nil {
		t.Fatalf("args del ChatJob: %v", err)
	}
	if chatArgs.RepositoryID != repo.ID || chatArgs.PullRequestID != pr.ID ||
		chatArgs.ParentCommentID != fmt.Sprintf("%d", glNoteChat) ||
		chatArgs.CommentBody != nota || chatArgs.CommentAuthor != "dev-integration" ||
		chatArgs.HeadSha != glHeadSHA || chatArgs.BaseSha != "tip-main" ||
		chatArgs.Language != repo.Language {
		t.Errorf("args del ChatJob incorrectos: %+v", chatArgs)
	}

	// -- 3. Ejecución del ChatJob: HandleChat con /explain --------------------
	chatIn := review.ChatInput{
		PullRequestID:   chatArgs.PullRequestID,
		RepositoryID:    chatArgs.RepositoryID,
		ParentCommentID: chatArgs.ParentCommentID,
		CommentBody:     chatArgs.CommentBody,
		CommentAuthor:   chatArgs.CommentAuthor,
		HeadSHA:         chatArgs.HeadSha,
		BaseSHA:         chatArgs.BaseSha,
		Language:        chatArgs.Language,
	}
	res, err := review.HandleChat(ctx, review.DefaultChatConfig(), st, gateway, vcsStub, chatIn)
	if err != nil {
		t.Fatalf("HandleChat (/explain): %v", err)
	}
	if res.Response != glChatReply || res.EnqueueReview {
		t.Errorf("respuesta del chat incorrecta: %+v", res)
	}

	// La respuesta se publica en el hilo (PostReply con el padre) y queda
	// registrada en comments_sent — la marca de idempotencia (§3.3).
	replies := vcsStub.snapshot()
	if len(replies) != 1 || replies[0].parent != chatArgs.ParentCommentID || replies[0].body != glChatReply {
		t.Errorf("PostReply incorrecto: %+v", replies)
	}
	sent, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "chat",
	})
	if err != nil {
		t.Fatalf("GetCommentsSentByPRAndType: %v", err)
	}
	if len(sent) != 1 || sent[0].CommentID != "reply-1" ||
		!sent[0].ParentCommentID.Valid || sent[0].ParentCommentID.String != chatArgs.ParentCommentID {
		t.Errorf("comments_sent (chat) incorrecto: %+v", sent)
	}

	// Idempotencia (§9.12): el mismo comentario padre re-ejecutado no
	// duplica la respuesta (el reintento del job corta por comments_sent).
	res, err = review.HandleChat(ctx, review.DefaultChatConfig(), st, gateway, vcsStub, chatIn)
	if err != nil {
		t.Fatalf("HandleChat (reintento): %v", err)
	}
	if res.Response != "" || res.EnqueueReview {
		t.Errorf("el reintento debe cortar sin respuesta: %+v", res)
	}
	if n := len(vcsStub.snapshot()); n != 1 {
		t.Errorf("el reintento no debe publicar de nuevo: replies=%d", n)
	}

	// -- 4. /review sobre el MR abierto: EnqueueReview, sin publicación -------
	res, err = review.HandleChat(ctx, review.DefaultChatConfig(), st, gateway, vcsStub, review.ChatInput{
		PullRequestID:   pr.ID,
		RepositoryID:    repo.ID,
		ParentCommentID: fmt.Sprintf("%d", glNoteRev),
		CommentBody:     "@" + glBotUsername + " /review",
		CommentAuthor:   "dev-integration",
		HeadSHA:         pr.HeadSha,
		BaseSHA:         pr.BaseSha,
		Language:        repo.Language,
	})
	if err != nil {
		t.Fatalf("HandleChat (/review): %v", err)
	}
	if !res.EnqueueReview || res.Response != "" {
		t.Errorf("/review en abierto debe encolar sin responder: %+v", res)
	}
	if n := len(vcsStub.snapshot()); n != 1 {
		t.Errorf("/review no debe publicar respuesta: replies=%d", n)
	}
	if sent, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "chat",
	}); err != nil || len(sent) != 1 {
		t.Errorf("/review no debe registrar comments_sent: %+v err=%v", sent, err)
	}

	// -- 5. Cierre del MR (webhook close) + /review: negativa sin encolar -----
	resp = postGitlabWebhook(t, apiSrv.URL, glMRPayload(glProjectID, glMRIID, "close", glHeadSHA, "main"), &deliveries)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("close: la API debe responder 2xx, fue %d", resp.StatusCode)
	}
	if jobs := q.pending(); len(jobs) != 0 {
		t.Errorf("close no debe encolar (MetricsJob es F5): encoló %+v", jobs)
	}
	pr, err = st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
		RepositoryID: repo.ID, Number: glMRIID,
	})
	if err != nil || pr.State != "closed" {
		t.Fatalf("el cierre debe dejar state=closed: %+v err=%v", pr, err)
	}

	res, err = review.HandleChat(ctx, review.DefaultChatConfig(), st, gateway, vcsStub, review.ChatInput{
		PullRequestID:   pr.ID,
		RepositoryID:    repo.ID,
		ParentCommentID: fmt.Sprintf("%d", glNoteRevX),
		CommentBody:     "@" + glBotUsername + " /review",
		CommentAuthor:   "dev-integration",
		HeadSHA:         pr.HeadSha,
		BaseSHA:         pr.BaseSha,
		Language:        repo.Language,
	})
	if err != nil {
		t.Fatalf("HandleChat (/review cerrado): %v", err)
	}
	if res.EnqueueReview {
		t.Errorf("/review en cerrado no debe encolar: %+v", res)
	}
	if !strings.Contains(res.Response, "cerrado") {
		t.Errorf("la negativa debe mencionar el cierre: %q", res.Response)
	}
	replies = vcsStub.snapshot()
	if len(replies) != 2 || replies[1].parent != fmt.Sprintf("%d", glNoteRevX) ||
		!strings.Contains(replies[1].body, "cerrado") {
		t.Errorf("la negativa debe publicarse en el hilo: %+v", replies)
	}
	sent, err = st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "chat",
	})
	if err != nil || len(sent) != 2 {
		t.Errorf("comments_sent debe acumular la negativa: %+v err=%v", sent, err)
	}

	// -- 6. Todas las deliveries quedaron registradas -------------------------
	for _, d := range deliveries {
		exists, err := st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{
			Vcs: "gitlab", DeliveryID: d,
		})
		if err != nil || !exists {
			t.Errorf("delivery %q debe estar registrada: exists=%v err=%v", d, exists, err)
		}
	}
}
