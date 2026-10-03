package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// gitlabCom es la base default de la instancia: self-managed la pisa por repo
// (columna base_url, guía §3.3).
const gitlabCom = "https://gitlab.com"

// Adapter implementa vcs.VCSProvider para GitLab (mapa: backend.vcs.gitlab).
// La autenticación es toda por repo: project access token para la REST
// (api_token cifrada — la deploy key solo clona, no sirve para la API),
// deploy key SSH para el clonado y signing token/secret token para verificar
// webhooks (§9.3) — nada global como el secret de la GitHub App.
type Adapter struct {
	st  *store.Store
	cfg *config.Config
	jq  jobs.JobQueue

	http    *http.Client
	maxBody int64 // tope de payload de webhook (§9.3)

	botUsername string        // anti-bucle del chat (§3.5: GitLab no expone flag de bot)
	branchTTL   time.Duration // TTL de la cache de tips de rama (§3.6.1.1)
	tolerance   time.Duration // frescura del timestamp Standard Webhooks (§9.3)

	mu   sync.Mutex
	tips map[string]branchTip // tip de rama por (proyecto|instancia|rama)
}

// branchTip es un valor cacheado del endpoint de branches (§3.6.1.1).
type branchTip struct {
	sha       string
	expiresAt time.Time
}

var _ vcs.VCSProvider = (*Adapter)(nil)

// New arma el adapter. Los topes y defaults de config se resuelven acá: la
// config de Stage2 es opcional al arranque y el adapter no debe degradar en
// silencio (sin bot username no hay anti-bucle, §3.5).
func New(st *store.Store, cfg *config.Config, jq jobs.JobQueue) *Adapter {
	timeout := cfg.Stage2.VCSTimeout
	if timeout <= 0 {
		timeout = config.DefaultVCSTimeout
	}
	maxBody := cfg.Stage2.WebhookMaxBytes
	if maxBody <= 0 {
		maxBody = config.DefaultWebhookMaxBytes
	}
	bot := cfg.Stage2.GitLabBotUsername
	if bot == "" {
		bot = config.DefaultGitLabBotUsername
	}
	branchTTL := cfg.Stage2.BranchTipCacheTTL
	if branchTTL <= 0 {
		branchTTL = config.DefaultBranchTipCacheTTL
	}
	tolerance := cfg.Stage2.WebhookTimestampTolerance
	if tolerance <= 0 {
		tolerance = config.DefaultWebhookTimestampTolerance
	}
	return &Adapter{
		st:          st,
		cfg:         cfg,
		jq:          jq,
		http:        &http.Client{Timeout: timeout},
		maxBody:     maxBody,
		botUsername: bot,
		branchTTL:   branchTTL,
		tolerance:   tolerance,
		tips:        map[string]branchTip{},
	}
}

// apiBase resuelve la base de la instancia del repo: default gitlab.com,
// overridable para self-managed (§3.3).
func (a *Adapter) apiBase(repo *store.Repository) string {
	if repo.BaseUrl.Valid && repo.BaseUrl.String != "" {
		return strings.TrimRight(repo.BaseUrl.String, "/")
	}
	return gitlabCom
}

// repoToken descifra el project access token del repo: autentica las llamadas
// REST del adapter (guía §3.3).
func (a *Adapter) repoToken(repo *store.Repository) (string, error) {
	return a.decryptSecret(repo.ApiToken, "api_token (project access token)")
}

// decryptSecret descifra una columna cifrada del repo (AES-256-GCM, §9.2).
func (a *Adapter) decryptSecret(col pgtype.Text, what string) (string, error) {
	if !col.Valid || col.String == "" {
		return "", fmt.Errorf("el repo no tiene %s configurado", what)
	}
	plain, err := store.Decrypt(a.cfg.MasterKey, col.String)
	if err != nil {
		return "", fmt.Errorf("descifrando %s: %w", what, err)
	}
	return string(plain), nil
}

// doJSON arma la request contra la API v4 de GitLab (PRIVATE-TOKEN), la
// ejecuta y decodea la respuesta JSON en out (si no es nil). Devuelve el
// status para clasificar 404 del flujo de negocio (§3.6: nota borrada →
// recrear).
func (a *Adapter) doJSON(ctx context.Context, method, rawURL, token string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("serializando request %s: %w", rawURL, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return 0, fmt.Errorf("construyendo request %s: %w", rawURL, err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decodificando respuesta %s: %w", req.URL.Path, err)
		}
	}
	return resp.StatusCode, nil
}

// branchTip resuelve el SHA actual de una rama (§3.6.1.1): el payload de
// GitLab no incluye ningún SHA de la rama base — el tip se consulta por API
// al procesar un evento que dispara review, con cache de TTL corto (llamada
// de metadatos acotada: lo vetado en el handler es LLM y análisis, §3.5).
func (a *Adapter) branchTip(ctx context.Context, repo *store.Repository, branch string) (string, error) {
	key := strconv.FormatInt(repo.ExternalID, 10) + "|" + a.apiBase(repo) + "|" + branch
	a.mu.Lock()
	if t, ok := a.tips[key]; ok && time.Now().Before(t.expiresAt) {
		a.mu.Unlock()
		return t.sha, nil
	}
	a.mu.Unlock()

	token, err := a.repoToken(repo)
	if err != nil {
		return "", fmt.Errorf("token para el tip de %s: %w", branch, err)
	}
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	path := fmt.Sprintf("%s/api/v4/projects/%d/repository/branches/%s",
		a.apiBase(repo), repo.ExternalID, url.PathEscape(branch))
	status, err := a.doJSON(ctx, http.MethodGet, path, token, nil, &out)
	if err != nil {
		return "", fmt.Errorf("tip de la rama %s: %w", branch, err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("tip de la rama %s: status %d", branch, status)
	}
	if out.Commit.ID == "" {
		return "", fmt.Errorf("la rama %s no informa commit", branch)
	}

	// ponytail: la cache no evicta — acotada por pares (repo, rama) reales,
	// decenas a escala single-org; subirla a un LRU si algún día crece.
	a.mu.Lock()
	a.tips[key] = branchTip{sha: out.Commit.ID, expiresAt: time.Now().Add(a.branchTTL)}
	a.mu.Unlock()
	return out.Commit.ID, nil
}

// apiError describe una respuesta no exitosa con un recorte del body.
func apiError(method, path string, status int, body io.Reader) error {
	b, _ := io.ReadAll(io.LimitReader(body, 512))
	return fmt.Errorf("%s %s: status %d: %s", method, path, status, string(b))
}
