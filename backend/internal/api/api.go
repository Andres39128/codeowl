// Package api es el routing y middleware del dashboard (mapa: backend.api):
// ServeMux stdlib, handlers REST de F0 (auth por sesión, §3.4) y healthcheck.
// Los webhooks VCS (autenticados por firma HMAC, sin cookie) aterrizan en F1/F2
// y quedan fuera del CSRF (§3.4) — se montarán en este mismo mux.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// Server agrupa las dependencias de los handlers HTTP.
type Server struct {
	store   *store.Store
	cfg     *config.Config
	limiter *limiter // backoff de login por usuario (§3.4), en memoria

	// Adapters VCS completos (mapa: backend.api.nota — depende de vcs):
	// montan el webhook (HandleWebhook) y sirven el diff del detalle de PR
	// (GetDiff, F3). La autenticación del webhook es la firma del payload
	// (§9.3), no la cookie de sesión.
	github vcs.VCSProvider // webhook + GetDiff de GitHub
	gitlab vcs.VCSProvider // webhook + GetDiff de GitLab

	queue jobs.JobQueue // encola ReconcileJob en la reconexión de repos (§3.5); nunca procesa
	llm   LLMTester     // settings: prueba de conexión (§3.5) + guard de dims embedding (§3.3)
}

// nopQueue es el default cuando nadie inyecta cola (tests que no tocan repos):
// los handlers jamás ven nil.
type nopQueue struct{}

func (nopQueue) Enqueue(context.Context, string, json.RawMessage) error { return nil }
func (nopQueue) CancelPendingByPR(context.Context, int64) (int, error)  { return 0, nil }
func (nopQueue) Register(...jobs.Worker) error                          { return nil }
func (nopQueue) Start(context.Context) error                            { return nil }
func (nopQueue) Stop(context.Context) error                             { return nil }

// New arma el Server. El limiter de login vive en el Server (memoria del
// proceso — §9.6: filosofía single-worker, suficiente para 1-5 usuarios).
// github y gitlab son los adapters VCS completos (webhook POST
// /webhooks/{github, gitlab} + GetDiff del detalle de PR, F3); queue y llm
// son opcionales (nil → no-op / 503 en la prueba de conexión).
func New(st *store.Store, cfg *config.Config, github, gitlab vcs.VCSProvider, queue jobs.JobQueue, llm LLMTester) *Server {
	if queue == nil {
		queue = nopQueue{}
	}
	return &Server{
		store:   st,
		cfg:     cfg,
		limiter: newLimiter(cfg.LoginMaxFails),
		github:  github,
		gitlab:  gitlab,
		queue:   queue,
		llm:     llm,
	}
}

// webhookHandler adapta un VCSProvider al mux: HandleWebhook recibe el ctx
// del request explícito (contrato vcs, §9.3), no es un http.Handler.
func webhookHandler(p vcs.VCSProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.HandleWebhook(r.Context(), w, r)
	})
}

// Routes arma el mux con la superficie REST (mapa: rest_endpoints): F0 auth,
// webhook GitHub de F1, settings/cola de F1 y detalle de PR de F3. El
// encadenado es: logging → recover → (auth → csrf, endpoints de sesión;
// auth → csrf → admin, settings; auth solo lectura, cola y PRs).
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", s.withLogging(s.withRecover(http.HandlerFunc(s.handleHealthz))))

	// Webhooks VCS: autenticados por firma (§9.3), sin cookie — fuera del
	// CSRF (§3.4). El handler valida, filtra y encola: jamás toca el gateway
	// LLM (§3.5).
	if s.github != nil {
		mux.Handle("POST /webhooks/github", s.withLogging(s.withRecover(webhookHandler(s.github))))
	}
	if s.gitlab != nil {
		mux.Handle("POST /webhooks/gitlab", s.withLogging(s.withRecover(webhookHandler(s.gitlab))))
	}

	// Login es mutante pero no autenticado por cookie: el CSRF de §3.4 no
	// aplica (la cookie SameSite=Lax + el rate limit son su defensa).
	mux.Handle("POST /api/auth/login", s.withLogging(s.withRecover(http.HandlerFunc(s.handleLogin))))

	// Logout y session requieren sesión vigente; logout es mutante → CSRF.
	mux.Handle("POST /api/auth/logout", s.withLogging(s.withRecover(s.withAuth(s.withCSRF(http.HandlerFunc(s.handleLogout))))))
	mux.Handle("GET /api/auth/session", s.withLogging(s.withRecover(s.withAuth(http.HandlerFunc(s.handleSession)))))

	// Settings: proveedores LLM, repos conectados y usuarios — admin-only
	// (§3.4). La pila completa es s.admin: logging → recover → auth → csrf
	// → RequireAdmin (el csrf pasa de largo los GET).
	mux.Handle("GET /api/providers", s.admin(s.handleListProviders))
	mux.Handle("POST /api/providers", s.admin(s.handleCreateProvider))
	mux.Handle("PUT /api/providers/{id}", s.admin(s.handleUpdateProvider))
	mux.Handle("DELETE /api/providers/{id}", s.admin(s.handleDeleteProvider))
	mux.Handle("POST /api/providers/{id}/test", s.admin(s.handleTestProvider))

	mux.Handle("GET /api/repos", s.admin(s.handleListRepos))
	mux.Handle("POST /api/repos", s.admin(s.handleCreateRepo))
	mux.Handle("PUT /api/repos/{id}", s.admin(s.handleUpdateRepo))
	// §3.5: la baja del repo es el flag enabled, jamás un delete.
	mux.Handle("DELETE /api/repos/{id}", s.admin(s.handleDeleteRepo))

	mux.Handle("GET /api/users", s.admin(s.handleListUsers))
	mux.Handle("POST /api/users", s.admin(s.handleInviteUser))
	mux.Handle("PUT /api/users/{id}", s.admin(s.handleUserAction))

	// Panel de cola (§9.9): cualquier usuario autenticado — el member opera
	// el triage y consulta el estado de la cola (§3.4). Solo lectura: auth
	// sin csrf.
	mux.Handle("GET /api/jobs", s.withLogging(s.withRecover(s.withAuth(http.HandlerFunc(s.handleListJobs)))))

	// Detalle de PR (guía §6 F3): lista, detalle con findings y diff vía
	// GetDiff del adapter. Solo lectura, member-visible (§3.4: opera el
	// triage y consulta PRs) — auth sin csrf.
	mux.Handle("GET /api/prs", s.withLogging(s.withRecover(s.withAuth(http.HandlerFunc(s.handleListPRs)))))
	mux.Handle("GET /api/prs/{id}", s.withLogging(s.withRecover(s.withAuth(http.HandlerFunc(s.handlePRDetail)))))
	mux.Handle("GET /api/prs/{id}/diff", s.withLogging(s.withRecover(s.withAuth(http.HandlerFunc(s.handlePRDiff)))))

	return mux
}

// handleHealthz responde 200 con JSON mínimo para el healthcheck de F0.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON serializa v como cuerpo JSON con el status dado.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("escribiendo respuesta json", "err", err)
	}
}

// writeError responde un error JSON uniforme: {"error": "..."}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
