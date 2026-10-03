package jobs

// Tests F1 de los workers (mapa: backend.jobs). Corren contra el Postgres de
// desarrollo (mismo patrón de jobs_test.go): requieren DATABASE_URL en el
// entorno (just test lo exporta del .env). Los stubs replican los contratos
// de internal/review (gateway por marcador de prompt, analyzer vacío) y del
// VCS (FetchPR graba el workdir, GetDiff sirve un diff fijo).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/index"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// wNano da un valor único por corrida para claves y SHAs (la BD es
// compartida: los sufijos evitan choques entre tests y ejecuciones).
func wNano() int64 { return time.Now().UnixNano() }

func wText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// wStore abre la store de desarrollo con migraciones aplicadas.
func wStore(t *testing.T) *store.Store {
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
	return st
}

// wRepo crea un repo GitHub efímero; el cleanup borra la fila (los PRs y
// reviews colgados se borran antes por FK, sin cascada).
func wRepo(t *testing.T, st *store.Store) store.Repository {
	t.Helper()
	repo, err := st.CreateRepository(context.Background(), store.CreateRepositoryParams{
		Vcs:        "github",
		ExternalID: wNano(),
		Owner:      "codeowl-tests",
		Name:       fmt.Sprintf("workers-%d", wNano()),
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	return repo
}

// wDropRepo limpia en orden FK: comments_sent → findings → reviews →
// pull_requests → repositories.
func wDropRepo(t *testing.T, st *store.Store, repoID int64) {
	t.Helper()
	ctx := context.Background()
	_, _ = st.Pool.Exec(ctx, `DELETE FROM comments_sent WHERE pull_request_id IN
		(SELECT id FROM pull_requests WHERE repository_id = $1)`, repoID)
	_, _ = st.Pool.Exec(ctx, `DELETE FROM findings WHERE review_id IN
		(SELECT r.id FROM reviews r JOIN pull_requests p ON p.id = r.pull_request_id
		 WHERE p.repository_id = $1)`, repoID)
	_, _ = st.Pool.Exec(ctx, `DELETE FROM reviews WHERE pull_request_id IN
		(SELECT id FROM pull_requests WHERE repository_id = $1)`, repoID)
	_, _ = st.Pool.Exec(ctx, "DELETE FROM pull_requests WHERE repository_id = $1", repoID)
	_, _ = st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", repoID)
}

// wPR crea un PR abierto con SHAs únicos por corrida.
func wPR(t *testing.T, st *store.Store, repoID, number int64) store.PullRequest {
	t.Helper()
	pr, err := st.UpsertPullRequest(context.Background(), store.UpsertPullRequestParams{
		RepositoryID: repoID,
		Number:       number,
		Author:       "autora",
		State:        "open",
		HeadSha:      fmt.Sprintf("head-%d", wNano()),
		BaseRef:      "main",
		BaseSha:      fmt.Sprintf("base-%d", wNano()),
		CreatedAt:    pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}
	return pr
}

// ---------------------------------------------------------------------------
// Stubs (§4.5: stubs de pocas líneas, sin mocks)
// ---------------------------------------------------------------------------

// wProvider sirve el diff fijo, graba el workdir del FetchPR y lista los PRs
// abiertos que le carguen. Lo que el worker no usa devuelve error: si se
// llama, el test lo tiene que ver. Con fetchDefault, FetchDefaultBranch arma
// un clon de juguete y graba el workdir (el IndexJob lo consume).
type wProvider struct {
	fetchWorkdir   string
	fetchDefault   bool
	defaultWorkdir string
	diff           string
	diffErr        error
	openPRs        []vcs.OpenPR
	timeline       *vcs.PRTimeline // FetchPRTimeline: nil → timeline vacía
	timelineErr    error
}

func (p *wProvider) HandleWebhook(context.Context, http.ResponseWriter, *http.Request) {
}
func (p *wProvider) FetchPR(_ context.Context, _ *store.Repository, _ *store.PullRequest, workdir string) error {
	p.fetchWorkdir = workdir
	return nil
}
func (p *wProvider) FetchDefaultBranch(_ context.Context, _ *store.Repository, workdir string) error {
	if !p.fetchDefault {
		return errors.New("wProvider: FetchDefaultBranch no esperado")
	}
	p.defaultWorkdir = workdir
	return wGitInit(workdir)
}
func (p *wProvider) FetchPRTimeline(context.Context, *store.Repository, *store.PullRequest) (*vcs.PRTimeline, error) {
	if p.timelineErr != nil {
		return nil, p.timelineErr
	}
	if p.timeline == nil {
		return &vcs.PRTimeline{}, nil
	}
	return p.timeline, nil
}
func (p *wProvider) GetDiff(context.Context, *store.Repository, *store.PullRequest) (string, error) {
	return p.diff, p.diffErr
}
func (p *wProvider) ListOpenPRs(context.Context, *store.Repository) ([]vcs.OpenPR, error) {
	return p.openPRs, nil
}
func (p *wProvider) PostInlineComment(context.Context, *store.Repository, *store.PullRequest, vcs.CommentPosition, string) (string, error) {
	return "inline-1", nil
}
func (p *wProvider) PostSuggestion(context.Context, *store.Repository, *store.PullRequest, vcs.CommentPosition, string) (string, error) {
	return "sug-1", nil
}
func (p *wProvider) PostSummary(context.Context, *store.Repository, *store.PullRequest, string, string) (string, error) {
	return fmt.Sprintf("sum-%d", wNano()), nil
}
func (p *wProvider) PostReply(context.Context, *store.Repository, *store.PullRequest, string, string) (string, error) {
	return "", errors.New("wProvider: PostReply no esperado")
}

// Marcadores del prompt de usuario de internal/review/agents.go (no
// exportados): enrutan la respuesta del gateway stub al agente correcto.
const (
	wSummarizerMarker = "Resumí el siguiente pull request."
	wReviewerOutput   = "[]" // diff sin hallazgos LLM: corrida válida y exitosa
	wSummarizerOutput = `{"summary":"resumen del PR","walkthrough":"","mermaid":""}`
)

// wGateway responde el reviewer (salida vacía válida) y el summarizer por
// el marcador del prompt de usuario.
type wGateway struct{}

func (wGateway) Complete(_ context.Context, _, _ string, user string) (string, error) {
	if strings.HasPrefix(user, wSummarizerMarker) {
		return wSummarizerOutput, nil
	}
	return wReviewerOutput, nil
}

// wAnalyzer devuelve un resultado vacío: SAST sin hallazgos.
type wAnalyzer struct{}

func (wAnalyzer) Run(context.Context, string) (*analyze.AnalysisResult, error) {
	return &analyze.AnalysisResult{}, nil
}

// wJob arma un Job River en memoria con los campos que los workers leen.
func wJob[T river.JobArgs](args T, attempt, maxAttempts int) *river.Job[T] {
	return &river.Job[T]{
		JobRow: &rivertype.JobRow{ID: wNano(), Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   args,
	}
}

// wGitInit arma un clon de juguete en el workdir (git init + commit de un
// main.go): index.Index resuelve el HEAD real del clon para el review.yaml
// (§6 F4), así que el stub de FetchDefaultBranch deja un repo válido.
func wGitInit(dir string) error {
	run := func(args ...string) error {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %v: %v: %s", args, err, out)
		}
		return nil
	}
	if err := run("init", "-q"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc Main() {}\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@codeowl.local"},
		{"config", "user.name", "codeowl-test"},
		{"add", "."},
		{"commit", "-qm", "fixture"},
	} {
		if err := run(args...); err != nil {
			return err
		}
	}
	return nil
}

// wQueue es el stub de JobQueue para los workers que encolan (ReviewJob →
// IndexJob §6 F4): registra los kinds encolados.
type wQueue struct {
	mu    sync.Mutex
	kinds []string
}

func (q *wQueue) Enqueue(_ context.Context, kind string, _ json.RawMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.kinds = append(q.kinds, kind)
	return nil
}
func (q *wQueue) CancelPendingByPR(context.Context, int64) (int, error) { return 0, nil }
func (q *wQueue) Register(...Worker) error                             { return nil }
func (q *wQueue) Start(context.Context) error {
	return nil
}
func (q *wQueue) Stop(context.Context) error { return nil }

// encolados devuelve los kinds grabados (copia segura).
func (q *wQueue) encolados() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.kinds...)
}

// wEmbedGateway graba los textos embebidos y sirve vectores de 1536 dims
// (stub del rol embedding, §4.5).
type wEmbedGateway struct {
	mu    sync.Mutex
	calls [][]string
}

func (g *wEmbedGateway) Embed(_ context.Context, texts []string) ([][]float32, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, texts)
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		v := make([]float32, 1536)
		v[0] = 0.5
		vecs[i] = v
	}
	return vecs, nil
}

// wExtractor devuelve el fixture fijo de símbolos (stub del sandbox).
type wExtractor struct{ syms []analyze.Symbol }

func (e *wExtractor) ExtractSymbols(context.Context, string) ([]analyze.Symbol, error) {
	return e.syms, nil
}

// wEmbeddingProvider crea un proveedor embedding enabled efímero en la BD:
// el core del índice re-valida el rol al correr (§9.6) — la BD compartida
// no garantiza que exista uno.
func wEmbeddingProvider(t *testing.T, st *store.Store) {
	t.Helper()
	clave := bytes.Repeat([]byte{0xC4}, 32)
	apiKey, err := store.Encrypt(clave, []byte("sk-embed-test"))
	if err != nil {
		t.Fatal(err)
	}
	prov, err := st.CreateLlmProvider(context.Background(), store.CreateLlmProviderParams{
		BaseUrl: "https://embed.test/v1", Model: fmt.Sprintf("embed-%d", wNano()),
		ApiKey: apiKey, Role: roleEmbedding, Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DeleteLlmProvider(context.Background(), prov.ID) })
}

// ---------------------------------------------------------------------------
// ReviewJobWorker
// ---------------------------------------------------------------------------

// Work corre la corrida completa con stubs: crea la review (success), pasa
// el workdir al FetchPR, lo borra al salir y re-encola el IndexJob del repo
// (§6 F4 — con proveedor embedding efímero en la BD).
func TestReviewJobWorkerCorridaYWorkdir(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	defer wDropRepo(t, st, repo.ID)
	pr := wPR(t, st, repo.ID, 101)
	wEmbeddingProvider(t, st)

	prov := &wProvider{diff: ""} // diff vacío: sin reviewer LLM, solo resumen
	cola := &wQueue{}
	w := &ReviewJobWorker{
		Store: st, Gateway: wGateway{}, Analyzer: wAnalyzer{}, Provider: prov, Queue: cola,
		Config: review.DefaultConfig(), WorkdirRoot: t.TempDir(),
	}

	err := w.Work(ctx, wJob(ReviewJobArgs{
		RepositoryID: repo.ID, PullRequestID: pr.ID,
		HeadSha: pr.HeadSha, BaseSha: pr.BaseSha,
	}, 1, reviewMaxAttempts))
	if err != nil {
		t.Fatalf("Work: %v", err)
	}

	// La corrida quedó success con su resumen.
	rev, err := st.GetLatestReviewByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetLatestReviewByPR: %v", err)
	}
	if rev.Status != review.StatusSuccess || rev.Summary == "" {
		t.Errorf("la corrida debe quedar success con resumen: got %+v", rev)
	}

	// §6 F4: success → re-encola el índice del repo.
	if kinds := cola.encolados(); len(kinds) != 1 || kinds[0] != KindIndex {
		t.Errorf("la review success debe encolar el IndexJob: %v", kinds)
	}

	// El workdir se creó para el clon y se borró al salir del job.
	if prov.fetchWorkdir == "" {
		t.Fatal("FetchPR no recibió workdir")
	}
	if _, err := os.Stat(prov.fetchWorkdir); !os.IsNotExist(err) {
		t.Errorf("el workdir del job debe limpiarse al salir: %s (%v)", prov.fetchWorkdir, err)
	}
}

// Al agotar intentos, el worker deja la corrida failed (§9.7): jamás una
// fila running huérfana. Antes del último intento, la running espera el
// reintento. Una corrida fallida NO encola el IndexJob (§6 F4: stale y
// failed no).
func TestReviewJobWorkerAgotaIntentos(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	defer wDropRepo(t, st, repo.ID)
	pr := wPR(t, st, repo.ID, 102)

	prov := &wProvider{diffErr: errors.New("VCS caído")}
	cola := &wQueue{}
	w := &ReviewJobWorker{
		Store: st, Gateway: wGateway{}, Analyzer: wAnalyzer{}, Provider: prov, Queue: cola,
		Config: review.DefaultConfig(), WorkdirRoot: t.TempDir(),
	}
	args := ReviewJobArgs{
		RepositoryID: repo.ID, PullRequestID: pr.ID,
		HeadSha: pr.HeadSha, BaseSha: pr.BaseSha,
	}

	// Intento intermedio: error devuelto para que River reintente; la
	// corrida sigue running.
	if err := w.Work(ctx, wJob(args, 1, reviewMaxAttempts)); err == nil {
		t.Fatal("un fallo de infraestructura debe devolver error para reintentar")
	}
	rev, err := st.GetLatestReviewByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetLatestReviewByPR: %v", err)
	}
	if rev.Status != review.StatusRunning {
		t.Errorf("antes del último intento la corrida sigue running: got %q", rev.Status)
	}

	// Último intento: error + corrida marcada failed.
	if err := w.Work(ctx, wJob(args, reviewMaxAttempts, reviewMaxAttempts)); err == nil {
		t.Fatal("el último intento también debe devolver error (River lo descarta)")
	}
	rev, err = st.GetLatestReviewByPR(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetLatestReviewByPR: %v", err)
	}
	if rev.Status != review.StatusFailed {
		t.Errorf("al agotar intentos la corrida queda failed (§9.7): got %q", rev.Status)
	}
	if kinds := cola.encolados(); len(kinds) != 0 {
		t.Errorf("una corrida que falla no encola IndexJob (§6 F4): %v", kinds)
	}
}

// ---------------------------------------------------------------------------
// CleanupJobWorker — retención (§9.11)
// ---------------------------------------------------------------------------

func TestCleanupJobWorkerRetencion(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()

	// Deliveries: una vieja (fuera de retención), una reciente.
	vieja, err := st.CreateWebhookDelivery(ctx, store.CreateWebhookDeliveryParams{
		Vcs: "github", DeliveryID: fmt.Sprintf("vieja-%d", wNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	reciente, err := st.CreateWebhookDelivery(ctx, store.CreateWebhookDeliveryParams{
		Vcs: "github", DeliveryID: fmt.Sprintf("reciente-%d", wNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	hace31Dias := time.Now().AddDate(0, 0, -31)
	if _, err := st.Pool.Exec(ctx,
		"UPDATE webhook_deliveries SET created_at = $1 WHERE id = $2", hace31Dias, vieja.ID); err != nil {
		t.Fatal(err)
	}

	// Sesiones: una expirada, una vigente (con su usuario, FK obligatoria).
	usuario, err := st.CreateUser(ctx, store.CreateUserParams{
		Username: fmt.Sprintf("wuser-%d", wNano()), Role: "member", PasswordHash: "hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenVieja := fmt.Sprintf("tok-vieja-%d", wNano())
	tokenViva := fmt.Sprintf("tok-viva-%d", wNano())
	if _, err := st.CreateSession(ctx, store.CreateSessionParams{
		TokenHash: tokenVieja, UserID: usuario.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSession(ctx, store.CreateSessionParams{
		TokenHash: tokenViva, UserID: usuario.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = st.Pool.Exec(ctx, "DELETE FROM webhook_deliveries WHERE id = ANY($1)", []int64{vieja.ID, reciente.ID})
		_, _ = st.Pool.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", usuario.ID)
		_, _ = st.Pool.Exec(ctx, "DELETE FROM users WHERE id = $1", usuario.ID)
	}()

	// Workdirs: un huérfano (2 h), uno fresco (review en curso posible).
	root := t.TempDir()
	huerfano := filepath.Join(root, fmt.Sprintf("rev-%d-huerfano", wNano()))
	fresco := filepath.Join(root, fmt.Sprintf("rev-%d-fresco", wNano()))
	for _, d := range []string{huerfano, fresco} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pasado := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(huerfano, pasado, pasado); err != nil {
		t.Fatal(err)
	}

	w := &CleanupJobWorker{Store: st, RetentionDays: 30, WorkdirRoot: root}
	if err := w.Work(ctx, wJob(CleanupJobArgs{}, 1, 1)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	// La vieja fuera, la reciente queda.
	ok, err := st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{Vcs: "github", DeliveryID: vieja.DeliveryID})
	if err != nil || ok {
		t.Errorf("la delivery fuera de retención debe borrarse: ok=%v err=%v", ok, err)
	}
	ok, err = st.WebhookDeliveryExists(ctx, store.WebhookDeliveryExistsParams{Vcs: "github", DeliveryID: reciente.DeliveryID})
	if err != nil || !ok {
		t.Errorf("la delivery reciente debe quedarse: ok=%v err=%v", ok, err)
	}

	// Sesión expirada fuera, vigente queda.
	if _, err := st.GetSessionByTokenHash(ctx, tokenVieja); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("la sesión expirada debe borrarse, got %v", err)
	}
	if _, err := st.GetSessionByTokenHash(ctx, tokenViva); err != nil {
		t.Errorf("la sesión vigente debe quedarse: %v", err)
	}

	// Workdir huérfano fuera, fresco queda.
	if _, err := os.Stat(huerfano); !os.IsNotExist(err) {
		t.Errorf("el workdir huérfano debe borrarse: %v", err)
	}
	if _, err := os.Stat(fresco); err != nil {
		t.Errorf("el workdir fresco debe quedarse: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RotationJobWorker — re-cifrado (§9.2)
// ---------------------------------------------------------------------------

func TestRotationJobWorkerRecifrado(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()

	oldKey := bytes.Repeat([]byte{0xA1}, 32) // claves de prueba de 32 bytes
	newKey := bytes.Repeat([]byte{0xB2}, 32)

	// La BD compartida acumula filas de corridas anteriores cuyas columnas
	// "cifradas" son plaintext de otros tests (el invariante de producción
	// es: columna cifrada = salida de store.Encrypt). El re-cifrado aborta
	// ante un valor indescifrable (§9.2: correcto en producción), así que
	// acá se repara la basura ANTES del job: solo filas anteriores al
	// arranque de esta corrida — las de tests corriendo en paralelo son
	// nuevas y no se tocan.
	reparaJunkCifrado(t, st, oldKey, time.Now().Add(-time.Second))

	apiKeyCifrada, err := store.Encrypt(oldKey, []byte("sk-prueba-llm"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := st.CreateLlmProvider(ctx, store.CreateLlmProviderParams{
		BaseUrl: "https://api.proveedor.test/v1", Model: fmt.Sprintf("modelo-%d", wNano()),
		ApiKey: apiKeyCifrada, Role: "review", Priority: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.DeleteLlmProvider(ctx, provider.ID)
	repo := wRepo(t, st)
	defer wDropRepo(t, st, repo.ID)
	// Webhook secret bajo la clave vieja; api token ya bajo la nueva (caso
	// de corrida a medias — el re-cifrado debe ser idempotente).
	whVieja, err := store.Encrypt(oldKey, []byte("signing-token"))
	if err != nil {
		t.Fatal(err)
	}
	apiNueva, err := store.Encrypt(newKey, []byte("project-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRepository(ctx, store.UpdateRepositoryParams{
		ID: repo.ID, Owner: repo.Owner, Name: repo.Name,
		WebhookSecret: wText(whVieja), ApiToken: wText(apiNueva),
		ReviewDrafts: repo.ReviewDrafts, Language: repo.Language, ChatOrgOnly: repo.ChatOrgOnly,
	}); err != nil {
		t.Fatal(err)
	}

	// Sin la clave previa el job se rehúsa a arrancar (§9.2): re-cifrar lo
	// que no se puede descifrar no es rotación.
	wSinPrevia := &RotationJobWorker{Store: st, NewKey: newKey}
	if err := wSinPrevia.Work(ctx, wJob(RotationJobArgs{}, 1, 1)); err == nil {
		t.Fatal("la rotación sin MASTER_KEY_PREVIOUS debe rehusarse (§9.2)")
	}

	w := &RotationJobWorker{Store: st, NewKey: newKey, OldKey: oldKey}
	if err := w.Work(ctx, wJob(RotationJobArgs{}, 1, 1)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	// El api_key del proveedor descifra con la clave nueva y conserva el
	// plaintext.
	got, err := st.GetLlmProvider(ctx, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := store.Decrypt(newKey, got.ApiKey)
	if err != nil {
		t.Fatalf("el api_key debe descifrar con la clave nueva: %v", err)
	}
	if string(plain) != "sk-prueba-llm" {
		t.Errorf("el plaintext debe conservarse: got %q", plain)
	}

	// Ambas columnas del repo descifran con la clave nueva.
	rGot, err := st.GetRepository(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err = store.Decrypt(newKey, rGot.WebhookSecret.String); err != nil || string(plain) != "signing-token" {
		t.Errorf("webhook_secret debe estar re-cifrado con la clave nueva: %q %v", plain, err)
	}
	if plain, err = store.Decrypt(newKey, rGot.ApiToken.String); err != nil || string(plain) != "project-token" {
		t.Errorf("api_token debe seguir descifrando con la clave nueva: %q %v", plain, err)
	}

	// Segunda corrida: idempotente — todo ya está bajo la clave nueva.
	if err := w.Work(ctx, wJob(RotationJobArgs{}, 1, 1)); err != nil {
		t.Fatalf("segunda rotación debe ser idempotente: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ReconcileJobWorker — reconexión (§3.5, sin LLM)
// ---------------------------------------------------------------------------

func TestReconcileJobWorkerCierraPRs(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	defer wDropRepo(t, st, repo.ID)
	prAbierto := wPR(t, st, repo.ID, 201) // sigue abierto en el VCS
	prCerrado := wPR(t, st, repo.ID, 202) // cerró durante la desconexión

	prov := &wProvider{openPRs: []vcs.OpenPR{{Number: prAbierto.Number}}}
	w := &ReconcileJobWorker{Store: st, Provider: prov}
	if err := w.Work(ctx, wJob(ReconcileJobArgs{RepositoryID: repo.ID}, 1, 1)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	got, err := st.GetPullRequest(ctx, prAbierto.ID)
	if err != nil || got.State != "open" {
		t.Errorf("el PR que sigue abierto en el VCS debe quedar open: %+v err=%v", got, err)
	}
	got, err = st.GetPullRequest(ctx, prCerrado.ID)
	if err != nil || got.State != "closed" {
		t.Errorf("el PR que ya no figura en el VCS debe marcarse closed: %+v err=%v", got, err)
	}

	// Un repo de otro VCS se rechaza en F1 (github es el único adapter).
	gl, err := st.CreateRepository(ctx, store.CreateRepositoryParams{
		Vcs: "gitlab", ExternalID: wNano(), Owner: "codeowl-tests", Name: fmt.Sprintf("gl-%d", wNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Pool.Exec(ctx, "DELETE FROM repositories WHERE id = $1", gl.ID)
	if err := w.Work(ctx, wJob(ReconcileJobArgs{RepositoryID: gl.ID}, 1, 1)); err == nil {
		t.Error("la reconciliación de un VCS sin adapter debe fallar en F1")
	}
}

// ---------------------------------------------------------------------------
// IndexJobWorker — índice RAG (§6 F4)
// ---------------------------------------------------------------------------

// Work indexa la rama default con stubs: el clon de juguete va por
// FetchDefaultBranch, la corrida embebe con el formato compartido, persiste
// en repo_index y el workdir se borra al salir.
func TestIndexJobWorkerCorridaYWorkdir(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	defer wDropRepo(t, st, repo.ID)
	// repo_index referencia al repo: la limpieza corre ANTES del drop (LIFO).
	defer st.Pool.Exec(ctx, "DELETE FROM repo_index WHERE repository_id = $1", repo.ID)
	wEmbeddingProvider(t, st) // el core re-valida el rol embedding al correr

	prov := &wProvider{fetchDefault: true}
	gw := &wEmbedGateway{}
	w := &IndexJobWorker{
		Store: st, Gateway: gw,
		Extractor: &wExtractor{syms: []analyze.Symbol{
			{File: "main.go", Symbol: "Main", Kind: "function", StartLine: 3, EndLine: 3},
		}},
		Provider: prov, Config: index.DefaultConfig(), WorkdirRoot: t.TempDir(),
	}

	if err := w.Work(ctx, wJob(IndexJobArgs{RepositoryID: repo.ID}, 1, reviewMaxAttempts)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	// El clon fue el workdir del job y se limpió al salir.
	if prov.defaultWorkdir == "" {
		t.Fatal("FetchDefaultBranch no recibió workdir")
	}
	if _, err := os.Stat(prov.defaultWorkdir); !os.IsNotExist(err) {
		t.Errorf("el workdir del job debe limpiarse al salir: %s (%v)", prov.defaultWorkdir, err)
	}
	// El texto embebido usa el formato compartido con Retrieve (T5).
	if len(gw.calls) != 1 || len(gw.calls[0]) != 1 || gw.calls[0][0] != "main.go\nfunction Main" {
		t.Errorf("Embed debió recibir el formato compartido: %v", gw.calls)
	}
	// La fila del símbolo quedó en repo_index.
	var n int
	if err := st.Pool.QueryRow(ctx,
		"SELECT COUNT(1) FROM repo_index WHERE repository_id = $1", repo.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("repo_index debe tener la fila del símbolo: n=%d", n)
	}
}

// El job de un repo deshabilitado no corre: skip con log, sin clon ni LLM
// (§3.5: solo repos conectados generan trabajo).
func TestIndexJobWorkerRepoDeshabilitado(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	defer wDropRepo(t, st, repo.ID)
	if _, err := st.SetRepositoryEnabled(ctx, store.SetRepositoryEnabledParams{ID: repo.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}

	prov := &wProvider{} // fetchDefault=false: si el guard falla, el error del stub lo delata
	w := &IndexJobWorker{
		Store: st, Gateway: &wEmbedGateway{}, Extractor: &wExtractor{},
		Provider: prov, Config: index.DefaultConfig(), WorkdirRoot: t.TempDir(),
	}
	if err := w.Work(ctx, wJob(IndexJobArgs{RepositoryID: repo.ID}, 1, reviewMaxAttempts)); err != nil {
		t.Fatalf("el job de un repo deshabilitado debe omitirse sin error: %v", err)
	}
	if prov.defaultWorkdir != "" {
		t.Error("un repo deshabilitado no debe clonarse")
	}
}

// Sin proveedor embedding la corrida NO quema reintentos: nil + log (§9.6,
// queda latente — el próximo trigger re-encola cuando el rol exista).
func TestIndexJobWorkerSinProveedorQuedaLatente(t *testing.T) {
	st := wStore(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	defer wDropRepo(t, st, repo.ID)
	restaurar := wSuspenderEmbedding(t, st)
	defer restaurar()

	prov := &wProvider{fetchDefault: true}
	w := &IndexJobWorker{
		Store: st, Gateway: &wEmbedGateway{},
		Extractor: &wExtractor{syms: []analyze.Symbol{
			{File: "main.go", Symbol: "Main", Kind: "function"},
		}},
		Provider: prov, Config: index.DefaultConfig(), WorkdirRoot: t.TempDir(),
	}
	if err := w.Work(ctx, wJob(IndexJobArgs{RepositoryID: repo.ID}, 1, reviewMaxAttempts)); err != nil {
		t.Fatalf("sin proveedor embedding el job queda latente, no falla: %v", err)
	}
}

// El JSON de los args del ReviewJob es el contrato con la API: los campos
// no deben renombrarse sin migrar los encolados en vuelo.
func TestReviewJobArgsJSON(t *testing.T) {
	b, err := json.Marshal(ReviewJobArgs{PullRequestID: 7, HeadSha: "h", BaseSha: "b"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pull_request_id", "head_sha", "base_sha"} {
		if _, ok := got[k]; !ok {
			t.Errorf("faltó la key %q en los args: %s", k, b)
		}
	}
	if _, ok := got["repository_id"]; ok {
		t.Error("repository_id en cero debe omitirse (compatibilidad con encolados viejos)")
	}
}

// reparaJunkCifrado restaura el invariante de producción en la BD compartida
// (columna cifrada = salida de store.Encrypt) sobre filas anteriores a
// inicio: un valor no vacío que no descifra con la clave vieja de prueba se
// re-envuelve como ciphertext de esa clave. Sin esto, el RotationJob aborta
// ante plaintext heredado de otros tests (comportamiento correcto en
// producción, §9.2).
func reparaJunkCifrado(t *testing.T, st *store.Store, oldKey []byte, inicio time.Time) {
	t.Helper()
	ctx := context.Background()

	descifrable := func(s string) bool {
		if s == "" {
			return true
		}
		_, err := store.Decrypt(oldKey, s)
		return err == nil
	}
	repara := func(table, col string, ids []int64, vals []pgtype.Text) {
		for i, v := range vals {
			if !v.Valid || descifrable(v.String) {
				continue
			}
			fixed, err := store.Encrypt(oldKey, []byte(v.String))
			if err != nil {
				t.Fatalf("reparando %s.%d.%s: %v", table, ids[i], col, err)
			}
			if _, err := st.Pool.Exec(ctx,
				fmt.Sprintf("UPDATE %s SET %s = $1 WHERE id = $2", table, col),
				fixed, ids[i]); err != nil {
				t.Fatalf("reparando %s.%d.%s: %v", table, ids[i], col, err)
			}
		}
	}

	rows, err := st.Pool.Query(ctx, `SELECT id, webhook_secret, secret_token, api_token, deploy_key
		FROM repositories WHERE created_at < $1`, inicio)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	var wh, stTok, api, dk []pgtype.Text
	for rows.Next() {
		var id int64
		var a, b, c, d pgtype.Text
		if err := rows.Scan(&id, &a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		wh = append(wh, a)
		stTok = append(stTok, b)
		api = append(api, c)
		dk = append(dk, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	repara("repositories", "webhook_secret", ids, wh)
	repara("repositories", "secret_token", ids, stTok)
	repara("repositories", "api_token", ids, api)
	repara("repositories", "deploy_key", ids, dk)

	prows, err := st.Pool.Query(ctx, `SELECT id, api_key FROM llm_providers WHERE created_at < $1`, inicio)
	if err != nil {
		t.Fatal(err)
	}
	defer prows.Close()
	var pids []int64
	var keys []pgtype.Text
	for prows.Next() {
		var id int64
		var k pgtype.Text
		if err := prows.Scan(&id, &k); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, id)
		keys = append(keys, k)
	}
	if err := prows.Err(); err != nil {
		t.Fatal(err)
	}
	repara("llm_providers", "api_key", pids, keys)
}

// ---------------------------------------------------------------------------
// indexRetriever — adaptador review.Retriever → index.Retrieve (§6 F4)
// ---------------------------------------------------------------------------

// wRetrieverStore satisface index.RetrieveStore con filas fijas: retrieval
// del adaptador sin BD (§4.5: stubs de pocas líneas en los bordes).
type wRetrieverStore struct {
	count    int64
	dims     int32
	gotLimit int32 // RowLimit recibido por la búsqueda (threads del topK)
	hits     []store.SearchRepoIndexBySimilarityRow
	files    []store.ListRepoIndexFilesRow
	byFiles  []store.ListRepoIndexByFilesRow
}

func (s *wRetrieverStore) CountRepoIndexByRepo(context.Context, int64) (int64, error) {
	return s.count, nil
}
func (s *wRetrieverStore) GetRepoIndexDims(context.Context) (int32, error) { return s.dims, nil }
func (s *wRetrieverStore) SearchRepoIndexBySimilarity(_ context.Context, arg store.SearchRepoIndexBySimilarityParams) ([]store.SearchRepoIndexBySimilarityRow, error) {
	s.gotLimit = arg.RowLimit
	return s.hits, nil
}
func (s *wRetrieverStore) ListRepoIndexFiles(context.Context, int64) ([]store.ListRepoIndexFilesRow, error) {
	return s.files, nil
}
func (s *wRetrieverStore) ListRepoIndexByFiles(context.Context, store.ListRepoIndexByFilesParams) ([]store.ListRepoIndexByFilesRow, error) {
	return s.byFiles, nil
}

// El adaptador convierte los RelatedSymbol de index al tipo propio de review
// con su vía intacta (similitud + expansión por imports) y arrastra el topK
// del wiring hasta el RowLimit del SQL (§6 F4, decisión 7).
func TestIndexRetrieverAdapter(t *testing.T) {
	st := &wRetrieverStore{
		count: 1, dims: 1536,
		hits: []store.SearchRepoIndexBySimilarityRow{
			{File: "util/query.go", Symbol: "Query", Kind: "func", StartLine: 10, EndLine: 20, Imports: []string{"util"}},
		},
		files: []store.ListRepoIndexFilesRow{{File: "util/db.go", FileHash: "h", EmbeddingModel: "m"}},
		byFiles: []store.ListRepoIndexByFilesRow{
			{File: "util/db.go", Symbol: "Conn", Kind: "type", StartLine: 5, EndLine: 9},
		},
	}
	retr := NewIndexRetriever(st, &wEmbedGateway{}, 8)

	syms, err := retr.RetrieveRelated(context.Background(), 42, "main.go", "package main")
	if err != nil {
		t.Fatalf("RetrieveRelated: %v", err)
	}
	if st.gotLimit != 8 {
		t.Errorf("RowLimit = %d, querés el topK del wiring (8)", st.gotLimit)
	}
	want := []review.RelatedSymbol{
		{File: "util/query.go", Symbol: "Query", Kind: "func", StartLine: 10, EndLine: 20, Via: "similar"},
		{File: "util/db.go", Symbol: "Conn", Kind: "type", StartLine: 5, EndLine: 9, Via: "import"},
	}
	if len(syms) != len(want) {
		t.Fatalf("símbolos convertidos = %d, querés %d: %+v", len(syms), len(want), syms)
	}
	for i, w := range want {
		if syms[i] != w {
			t.Errorf("símbolo %d = %+v, querés %+v", i, syms[i], w)
		}
	}
}

// Índice vacío: el adaptador devuelve (nil, nil) sin llamar al gateway — el
// bloque de contexto simplemente no existe (§6 F4: silencioso).
func TestIndexRetrieverAdapterIndiceVacio(t *testing.T) {
	st := &wRetrieverStore{}
	retr := NewIndexRetriever(st, &wEmbedGateway{}, 8)
	syms, err := retr.RetrieveRelated(context.Background(), 42, "main.go", "package main")
	if err != nil || syms != nil {
		t.Errorf("con índice vacío querés (nil, nil): got (%v, %v)", syms, err)
	}
}

// ---------------------------------------------------------------------------
// MetricsJobWorker (§6 F5)
// ---------------------------------------------------------------------------

// mInline siembra un comentario inline con huella y devuelve la fila.
func mInline(t *testing.T, st *store.Store, prID, revID int64, commentID, category, anchor string) store.CommentsSent {
	t.Helper()
	cs, err := st.CreateCommentSent(context.Background(), store.CreateCommentSentParams{
		PullRequestID: prID, ReviewID: pgtype.Int8{Int64: revID, Valid: true},
		CommentID: commentID, Type: "inline",
		File:     pgtype.Text{String: "main.go", Valid: true},
		Category: pgtype.Text{String: category, Valid: true},
		Anchor:   pgtype.Text{String: anchor, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateCommentSent(%s): %v", commentID, err)
	}
	return cs
}

// MetricsJobWorker outcome (§6 F5): resolved se escribe desde los threads del
// VCS matcheando comment_id (hilos ajenos se ignoran); applied sale de la
// heurística de contenido — sugerencia en las líneas añadidas de un commit
// POSTERIOR al comentario — y el hallazgo matcheado lleva el mismo valor en
// accepted. El commit anterior al comentario no aplica (skew de 5 min) y el
// comentario sin hallazgo con sugerencia queda en null (sin evaluación
// honesta).
func TestMetricsJobWorkerOutcome(t *testing.T) {
	_, st := testQueue(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	pr := wPR(t, st, repo.ID, 1)
	t.Cleanup(func() { wDropRepo(t, st, repo.ID) })
	if _, err := st.UpdatePullRequestState(ctx, store.UpdatePullRequestStateParams{
		ID: pr.ID, State: "closed", MergedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	rev, err := st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h1", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	fAcc, err := st.CreateFinding(ctx, store.CreateFindingParams{
		ReviewID: rev.ID, File: "main.go", Line: 10, Severity: "high",
		Category: "security", Body: "body-sec", Source: "llm",
		Suggestion: pgtype.Text{String: "db.Query(q, arg)", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	fViejo, err := st.CreateFinding(ctx, store.CreateFindingParams{
		ReviewID: rev.ID, File: "main.go", Line: 30, Severity: "low",
		Category: "tests", Body: "body-test", Source: "llm",
		Suggestion: pgtype.Text{String: "fixViejo()", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFinding(ctx, store.CreateFindingParams{
		ReviewID: rev.ID, File: "main.go", Line: 20, Severity: "medium",
		Category: "performance", Body: "body-perf", Source: "llm",
	}); err != nil {
		t.Fatal(err)
	}

	cs1 := mInline(t, st, pr.ID, rev.ID, "t-1", "security", "10")
	cs2 := mInline(t, st, pr.ID, rev.ID, "t-2", "performance", "20")
	cs3 := mInline(t, st, pr.ID, rev.ID, "t-3", "tests", "30")

	prov := &wProvider{timeline: &vcs.PRTimeline{
		Threads: []vcs.PRThread{
			{CommentID: "t-1", Resolved: true},
			{CommentID: "t-2", Resolved: false},
			{CommentID: "ajeno", Resolved: true}, // hilo que no es del bot: ignorado
		},
		Commits: []vcs.PRCommit{
			{
				SHA: "nuevo", AuthoredAt: time.Now(), // posterior al comentario
				Patch: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -10,1 +10,1 @@\n-old()\n+\tdb.Query(q, arg)\n",
			},
			{
				SHA: "viejo", AuthoredAt: time.Now().Add(-time.Hour), // anterior: fuera del skew
				Patch: "diff --git a/main_test.go b/main_test.go\n--- a/main_test.go\n+++ b/main_test.go\n@@ -1,1 +1,2 @@\n+fixViejo()\n",
			},
		},
	}}
	w := &MetricsJobWorker{Store: st, Provider: prov}
	if err := w.Work(ctx, wJob(MetricsJobArgs{RepositoryID: repo.ID, PullRequestID: pr.ID}, 1, metricsMaxAttempts)); err != nil {
		t.Fatalf("Work: %v", err)
	}

	inlines, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "inline",
	})
	if err != nil {
		t.Fatal(err)
	}
	porID := make(map[string]store.CommentsSent, len(inlines))
	for _, cs := range inlines {
		porID[cs.CommentID] = cs
	}
	if got := porID[cs1.CommentID]; !got.Resolved.Valid || !got.Resolved.Bool ||
		!got.Applied.Valid || !got.Applied.Bool {
		t.Errorf("t-1: resolved∧applied esperados true: got %+v", got)
	}
	if got := porID[cs2.CommentID]; !got.Resolved.Valid || got.Resolved.Bool || got.Applied.Valid {
		t.Errorf("t-2: resolved=false y applied NULL (hallazgo sin sugerencia): got %+v", got)
	}
	if got := porID[cs3.CommentID]; got.Resolved.Valid || !got.Applied.Valid || got.Applied.Bool {
		t.Errorf("t-3: resolved NULL (sin thread) y applied=false (commit anterior): got %+v", got)
	}

	// Espejo en findings: el matcheado aceptado; el del commit viejo no.
	findings, err := st.ListFindingsByPR(ctx, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	aceptados := map[int64]bool{}
	for _, f := range findings {
		if f.Accepted.Valid {
			aceptados[f.ID] = f.Accepted.Bool
		}
	}
	if !aceptados[fAcc.ID] {
		t.Errorf("finding %d debe quedar accepted=true (sugerencia en el commit nuevo)", fAcc.ID)
	}
	if val, ok := aceptados[fViejo.ID]; !ok || val {
		t.Errorf("finding %d debe quedar accepted=false (su commit es anterior al comentario)", fViejo.ID)
	}
}

// Guard del estado (§6 F5): un PR reabierto tras el cierre no tiene timeline
// final — skip honesto sin error y SIN tocar el proveedor (el error del stub
// delata cualquier llamada) ni las filas.
func TestMetricsJobWorkerReabiertoSeSalta(t *testing.T) {
	_, st := testQueue(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	pr := wPR(t, st, repo.ID, 2) // sigue open: wPR nace open
	t.Cleanup(func() { wDropRepo(t, st, repo.ID) })

	rev, err := st.CreateReview(ctx, store.CreateReviewParams{PullRequestID: pr.ID, HeadSha: "h1", BaseSha: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	mInline(t, st, pr.ID, rev.ID, "t-open", "security", "1")
	prov := &wProvider{timelineErr: errors.New("wProvider: FetchPRTimeline no esperado")}
	w := &MetricsJobWorker{Store: st, Provider: prov}
	if err := w.Work(ctx, wJob(MetricsJobArgs{RepositoryID: repo.ID, PullRequestID: pr.ID}, 1, metricsMaxAttempts)); err != nil {
		t.Fatalf("el guard debe saltar sin error: %v", err)
	}
	filas, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: pr.ID, Type: "inline",
	})
	if err != nil || len(filas) != 1 {
		t.Fatalf("GetCommentsSentByPRAndType: n=%d err=%v", len(filas), err)
	}
	if filas[0].Resolved.Valid {
		t.Errorf("el PR abierto no escribe outcomes: got %+v", filas[0].Resolved)
	}
}

// La timeline que falla es un error de verdad: River reintenta (×3) y al
// agotar deja discarded — jamás outcomes sobre una timeline a medias (§6 F5).
func TestMetricsJobWorkerTimelineFalla(t *testing.T) {
	_, st := testQueue(t)
	ctx := context.Background()
	repo := wRepo(t, st)
	pr := wPR(t, st, repo.ID, 3)
	t.Cleanup(func() { wDropRepo(t, st, repo.ID) })
	if _, err := st.UpdatePullRequestState(ctx, store.UpdatePullRequestStateParams{
		ID: pr.ID, State: "closed", MergedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	prov := &wProvider{timelineErr: errors.New("vcs caído")}
	w := &MetricsJobWorker{Store: st, Provider: prov}
	if err := w.Work(ctx, wJob(MetricsJobArgs{RepositoryID: repo.ID, PullRequestID: pr.ID}, 1, metricsMaxAttempts)); err == nil {
		t.Error("el fallo de la timeline debe pedir reintento (error)")
	}
}
