package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	vcsgh "github.com/Andres39128/codeowl/backend/internal/vcs/github"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// E2E contra el Postgres de desarrollo (mapa: pruebas del backend.api):
// requieren DATABASE_URL en el entorno (just test lo exporta del .env).
// El server de prueba es TLS (httptest.NewTLSServer) para que la cookie
// Secure de §3.4 circule de verdad por el cliente HTTP.

type testEnv struct {
	ts     *httptest.Server
	st     *store.Store
	user   string // username del usuario de prueba
	pass   string // contraseña en claro del usuario de prueba
	csrf   string // token CSRF de la última sesión logueada
	cookie *http.Cookie
	llm    *stubLLM   // gateway LLM falso: la prueba de conexión lee .err
	queue  *stubQueue // cola falsa: registra los encolados
}

// newTestEnv arma api + server TLS con un usuario de prueba fresco.
// maxFails parametriza el backoff de login para el test de rate limit.
func newTestEnv(t *testing.T, maxFails int) *testEnv {
	return newTestEnvVCS(t, maxFails, nil, nil)
}

// newTestEnvVCS es la variante para los endpoints que consumen adapters VCS
// (GetDiff del detalle de PR, F3): inyecta stubs como github/gitlab.
func newTestEnvVCS(t *testing.T, maxFails int, gh, gl vcs.VCSProvider) *testEnv {
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

	user := fmt.Sprintf("api-test-%d", time.Now().UnixNano())
	const pass = "clave-larga-de-test-123"
	hash, err := store.HashPassword(pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(context.Background(), store.CreateUserParams{
		Username:     user,
		Role:         "admin",
		PasswordHash: hash,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cfg := &config.Config{SessionTTL: time.Hour, LoginMaxFails: maxFails, MasterKey: make([]byte, 32)}
	env := &testEnv{st: st, user: user, pass: pass, llm: &stubLLM{}, queue: &stubQueue{}}
	srv := New(st, cfg, gh, gl, env.queue, env.llm)
	env.ts = httptest.NewTLSServer(srv.Routes())
	t.Cleanup(env.ts.Close)
	return env
}

// do manda un request JSON y devuelve la respuesta (el caller hace Close).
func (e *testEnv) do(t *testing.T, method, path string, cookie *http.Cookie, csrf string, body any) *http.Response {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set(csrfHeader, csrf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// login ejecuta el login y guarda cookie + csrf en el env si fue 200.
func (e *testEnv) login(t *testing.T, user, pass string) *http.Response {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/api/auth/login", nil, "", credentials{Username: user, Password: pass})
	if resp.StatusCode == http.StatusOK {
		var out authResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decodificando login: %v", err)
		}
		e.csrf = out.CSRFToken
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookie {
				e.cookie = c
			}
		}
	}
	return resp
}

func TestLoginOKSeteaCookieYDevuelveUsuario(t *testing.T) {
	e := newTestEnv(t, 5)

	resp := e.login(t, e.user, e.pass)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login válido debe ser 200, fue %d", resp.StatusCode)
	}
	// Cookie con los atributos de §3.4.
	c := e.cookie
	if c == nil {
		t.Fatal("el login debe setear la cookie de sesión")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("la cookie debe ser HttpOnly+Secure+SameSite=Lax: %+v", c)
	}
	if c.MaxAge != int(time.Hour/time.Second) {
		t.Errorf("la cookie debe expirar con el TTL de sesión (%ds), tiene MaxAge=%d", time.Hour/time.Second, c.MaxAge)
	}
	// Cuerpo: usuario + csrf usable.
	if e.csrf == "" {
		t.Error("el login debe devolver un csrf_token no vacío")
	}
}

func TestLoginPasswordIncorrecta(t *testing.T) {
	e := newTestEnv(t, 5)
	if got := e.login(t, e.user, "no-es-la-clave").StatusCode; got != http.StatusUnauthorized {
		t.Errorf("password incorrecta debe ser 401, fue %d", got)
	}
	// Usuario inexistente: mismo 401, sin distinguir (no enumerar usuarios).
	otro := newTestEnv(t, 5)
	if got := otro.login(t, "nadie-"+e.user, "x").StatusCode; got != http.StatusUnauthorized {
		t.Errorf("usuario inexistente debe ser 401, fue %d", got)
	}
}

func TestLoginUsuarioDeshabilitado(t *testing.T) {
	e := newTestEnv(t, 5)
	ctx := context.Background()
	if _, err := e.st.Pool.Exec(ctx, "UPDATE users SET disabled = true WHERE username = $1", e.user); err != nil {
		t.Fatal(err)
	}
	// Contraseña correcta pero cuenta deshabilitada (§3.4: la baja revoca acceso).
	if got := e.login(t, e.user, e.pass).StatusCode; got != http.StatusForbidden {
		t.Errorf("usuario deshabilitado debe ser 403, fue %d", got)
	}
	// Sin sesión creada: la session queda 401.
	if got := e.do(t, http.MethodGet, "/api/auth/session", e.cookie, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("sin login válido, session debe ser 401, fue %d", got)
	}
}

func TestLoginBackoffTrasNFallos(t *testing.T) {
	e := newTestEnv(t, 2) // N=2 para no alargar el test

	if got := e.login(t, e.user, "mal-1").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("fallo 1 debe ser 401, fue %d", got)
	}
	if got := e.login(t, e.user, "mal-2").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("fallo 2 debe ser 401, fue %d", got)
	}
	// N alcanzado: hasta la contraseña correcta queda en 429 (backoff, §3.4).
	resp := e.login(t, e.user, e.pass)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("tras %d fallos el login debe ser 429 aunque la clave sea correcta, fue %d", 2, resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("el 429 debe anunciar Retry-After")
	}
}

func TestSessionConYSinCookie(t *testing.T) {
	e := newTestEnv(t, 5)
	e.login(t, e.user, e.pass)

	resp := e.do(t, http.MethodGet, "/api/auth/session", e.cookie, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session con cookie debe ser 200, fue %d", resp.StatusCode)
	}
	var out authResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.User.Username != e.user || out.User.Role != "admin" {
		t.Errorf("session debe devolver el usuario de la sesión: %+v", out.User)
	}
	if out.CSRFToken == "" {
		t.Error("session debe devolver csrf_token para los mutantes posteriores")
	}

	// Sin cookie → 401.
	if got := e.do(t, http.MethodGet, "/api/auth/session", nil, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("session sin cookie debe ser 401, fue %d", got)
	}
	// Cookie inventada (sesión inexistente) → 401.
	fantasma := &http.Cookie{Name: sessionCookie, Value: "token-que-no-existe"}
	if got := e.do(t, http.MethodGet, "/api/auth/session", fantasma, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("session con token inexistente debe ser 401, fue %d", got)
	}
}

func TestCSRFRechazaMutanteSinToken(t *testing.T) {
	e := newTestEnv(t, 5)
	e.login(t, e.user, e.pass)

	// Mutante sin header → 403.
	if got := e.do(t, http.MethodPost, "/api/auth/logout", e.cookie, "", nil).StatusCode; got != http.StatusForbidden {
		t.Errorf("logout sin csrf debe ser 403, fue %d", got)
	}
	// Mutante con token equivocado → 403.
	if got := e.do(t, http.MethodPost, "/api/auth/logout", e.cookie, "token-falso", nil).StatusCode; got != http.StatusForbidden {
		t.Errorf("logout con csrf inválido debe ser 403, fue %d", got)
	}
	// El 403 no revocó nada: la sesión sigue viva.
	if got := e.do(t, http.MethodGet, "/api/auth/session", e.cookie, "", nil).StatusCode; got != http.StatusOK {
		t.Errorf("un csrf rechazado no debe revocar la sesión, session fue %d", got)
	}
	// Mutante sin sesión → 401 (auth antes que csrf).
	if got := e.do(t, http.MethodPost, "/api/auth/logout", nil, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("logout sin sesión debe ser 401, fue %d", got)
	}
}

func TestLogoutRevoca(t *testing.T) {
	e := newTestEnv(t, 5)
	e.login(t, e.user, e.pass)

	resp := e.do(t, http.MethodPost, "/api/auth/logout", e.cookie, e.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout debe ser 200, fue %d", resp.StatusCode)
	}
	// La sesión vieja murió (§3.4: logout revoca).
	if got := e.do(t, http.MethodGet, "/api/auth/session", e.cookie, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("tras el logout, session debe ser 401, fue %d", got)
	}
	// La cookie de logout viene vencida (Max-Age < 0).
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 {
			t.Errorf("logout debe vencer la cookie, llegó MaxAge=%d", c.MaxAge)
		}
	}
	// Verificación a nivel BD: no queda fila de sesión para ese usuario.
	var n int
	if err := e.st.Pool.QueryRow(context.Background(),
		"SELECT COUNT(1) FROM sessions s JOIN users u ON u.id = s.user_id WHERE u.username = $1", e.user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("logout debe borrar la fila de sesión en BD, quedan %d", n)
	}
}

// Cuerpo malformado: 400 sin tocar la BD ni el limiter.
func TestLoginCuerpoInvalido(t *testing.T) {
	e := newTestEnv(t, 5)
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/auth/login", bytes.NewBufferString("{no-json"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("cuerpo no-json debe ser 400, fue %d", resp.StatusCode)
	}
}

// Webhook firmado simulado (mapa: pruebas de backend.api): la ruta
// /webhooks/github está montada con logging+recover, SIN auth de cookie ni
// CSRF (§3.4) — su autenticación es la firma HMAC (§9.3).
func TestWebhookGitHubMontadoSinCSRF(t *testing.T) {
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

	const secret = "secreto-del-webhook-api-test"
	cfg := &config.Config{
		SessionTTL:    time.Hour,
		LoginMaxFails: 5,
		Stage2:        config.Stage2Config{GitHubWebhookSecret: secret},
	}
	gh := vcsgh.New(st, cfg, &stubQueue{})
	srv := httptest.NewServer(New(st, cfg, gh, nil, &stubQueue{}, &stubLLM{}).Routes())
	t.Cleanup(srv.Close)

	// Ping firmado: evento suscrito por el filtro de descarte — sin BD,
	// sin job: 200 prueba ruta + firma + ausencia de CSRF.
	body := []byte(`{"zen":"todo sale bien","hook_id":1,"sender":{"login":"alguien"}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("api-test-%d", time.Now().UnixNano()))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ping firmado debe ser 200, fue %d", resp.StatusCode)
	}

	// Sin firma: 401 — el CSRF de sesión no aplica acá, la HMAC manda.
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-GitHub-Event", "ping")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("webhook sin firma debe ser 401, fue %d", resp.StatusCode)
	}
}

// stubQueue es el mínimo jobs.JobQueue para los handlers: registra los
// encolados (kind + args) para que los tests verifiquen qué se encoló.
type stubQueue struct {
	mu       sync.Mutex
	kinds    []string
	payloads []json.RawMessage
}

func (q *stubQueue) Enqueue(_ context.Context, kind string, args json.RawMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.kinds = append(q.kinds, kind)
	q.payloads = append(q.payloads, args)
	return nil
}
func (q *stubQueue) Register(...jobs.Worker) error { return nil }
func (q *stubQueue) Start(context.Context) error   { return nil }
func (q *stubQueue) Stop(context.Context) error    { return nil }

func (q *stubQueue) enqueued(t *testing.T) ([]string, []json.RawMessage) {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.kinds...), append([]json.RawMessage(nil), q.payloads...)
}

// stubLLM satisface LLMTester: TestConnection falla solo si .err != nil;
// TestEmbedConnection (el guard de dims del rol embedding, §3.3) devuelve
// .embedDims salvo que .embedErr esté seteado, y registra cada probe con su
// api_key para las aserciones del guard.
type stubLLM struct {
	err       error
	embedErr  error
	embedDims int

	mu     sync.Mutex
	probes int
	probed string // última api_key sondeada (vacía = sin probes)
}

func (s *stubLLM) TestConnection(context.Context, string, string, string, string) error {
	return s.err
}

func (s *stubLLM) TestEmbedConnection(_ context.Context, _, apiKey, _ string) (int, int64, error) {
	s.mu.Lock()
	s.probes++
	s.probed = apiKey
	s.mu.Unlock()
	if s.embedErr != nil {
		return 0, 0, s.embedErr
	}
	return s.embedDims, 0, nil
}

func (s *stubLLM) probeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes
}

func (s *stubLLM) probedKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probed
}
