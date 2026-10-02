package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// Constantes de dominio del proveedor (§4.2): GitHub.com no tiene
// instancias self-managed — la URL de API no es configurable.
const (
	gitHubAPIURL     = "https://api.github.com"
	gitHubAPIVersion = "2022-11-28"
	mediaTypeJSON    = "application/vnd.github+json"
	mediaTypeDiff    = "application/vnd.github.diff"
)

// Adapter implementa vcs.VCSProvider para GitHub (mapa: backend.vcs.github).
type Adapter struct {
	st  *store.Store
	cfg *config.Config
	jq  jobs.JobQueue

	http    *http.Client
	baseURL string // sobrescribible en tests (stub server)
	maxBody int64  // tope de payload de webhook (§9.3)

	mu     sync.Mutex
	tokens *tokenManager // perezosa: las credenciales validan al conectar el primer repo
}

var _ vcs.VCSProvider = (*Adapter)(nil)

// New arma el adapter. No valida credenciales de la App al arranque: en F0
// el stack corre sin ellas y validan al conectar el primer repo (§6 F1) —
// el token manager se construye perezoso en la primera operación que la
// necesita.
func New(st *store.Store, cfg *config.Config, jq jobs.JobQueue) *Adapter {
	timeout := cfg.Stage2.VCSTimeout
	if timeout <= 0 {
		timeout = config.DefaultVCSTimeout
	}
	maxBody := cfg.Stage2.WebhookMaxBytes
	if maxBody <= 0 {
		maxBody = config.DefaultWebhookMaxBytes
	}
	return &Adapter{
		st:      st,
		cfg:     cfg,
		jq:      jq,
		http:    &http.Client{Timeout: timeout},
		baseURL: gitHubAPIURL,
		maxBody: maxBody,
	}
}

// ensureTokenManager construye el token manager la primera vez que se
// necesita (lazy: el arranque no exige credenciales de la App).
func (a *Adapter) ensureTokenManager() (*tokenManager, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokens != nil {
		return a.tokens, nil
	}
	if a.cfg.Stage2.GitHubAppID == "" || a.cfg.Stage2.GitHubAppPrivateKey == "" {
		return nil, errors.New("credenciales de la GitHub App no configuradas (GITHUB_APP_ID / GITHUB_APP_PRIVATE_KEY)")
	}
	tm, err := newTokenManager(a.cfg.Stage2.GitHubAppID, a.cfg.Stage2.GitHubAppPrivateKey, a.http, a.baseURL)
	if err != nil {
		return nil, err
	}
	a.tokens = tm
	return tm, nil
}

// repoToken resuelve el token de instalación para las llamadas API del repo:
// installation ID del repo (cacheado) → token efímero de instalación.
func (a *Adapter) repoToken(ctx context.Context, repo *store.Repository) (string, error) {
	tm, err := a.ensureTokenManager()
	if err != nil {
		return "", err
	}
	instID, err := tm.InstallationID(ctx, repo)
	if err != nil {
		return "", err
	}
	return tm.Token(ctx, instID)
}

// newAPIRequest arma la request REST a la API de GitHub: helper compartido
// por el adapter y el token manager (misma autenticación y versionado).
func newAPIRequest(ctx context.Context, method, path, token, accept string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("serializando request %s %s: %w", method, path, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, rd)
	if err != nil {
		return nil, fmt.Errorf("construyendo request %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", gitHubAPIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do ejecuta una request contra la API y decodea la respuesta JSON en out
// (si no es nil). Devuelve el status para clasificar 404/422 del flujo de
// negocio (§3.6: comentario borrado → recrear).
func (a *Adapter) do(ctx context.Context, req *http.Request, out any) (int, error) {
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decodificando respuesta %s %s: %w", req.Method, req.URL.Path, err)
		}
	}
	return resp.StatusCode, nil
}

// apiError describe una respuesta no exitosa con un recorte del body.
func apiError(method, path string, status int, body io.Reader) error {
	b, _ := io.ReadAll(io.LimitReader(body, 512))
	return fmt.Errorf("%s %s: status %d: %s", method, path, status, string(b))
}
