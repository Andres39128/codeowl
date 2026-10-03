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
)

// Server agrupa las dependencias de los handlers HTTP.
type Server struct {
	store   *store.Store
	cfg     *config.Config
	limiter *limiter      // backoff de login por usuario (§3.4), en memoria
	github  http.Handler  // webhook de GitHub (firma HMAC, sin cookie ni CSRF)
	gitlab  http.Handler  // webhook de GitLab (Standard Webhooks, sin cookie ni CSRF)
	queue   jobs.JobQueue // encola ReconcileJob en la reconexión de repos (§3.5); nunca procesa
	llm     LLMTester     // solo la prueba de conexión de settings (§3.5)
}

// nopQueue es el default cuando nadie inyecta cola (tests que no tocan repos):
// los handlers jamás ven nil.
type nopQueue struct{}

func (nopQueue) Enqueue(context.Context, string, json.RawMessage) error { return nil }
func (nopQueue) Register(...jobs.Worker) error                          { return nil }
func (nopQueue) Start(context.Context) error                            { return nil }
func (nopQueue) Stop(context.Context) error                             { return nil }

// New arma el Server. El limiter de login vive en el Server (memoria del
// proceso — §9.6: filosofía single-worker, suficiente para 1-5 usuarios).
// githubWebhook y gitlabWebhook son los handlers de POST /webhooks/{github,
// gitlab} (adapters VCS); queue y llm son opcionales (nil → no-op / 503 en
// la prueba de conexión).
func New(st *store.Store, cfg *config.Config, githubWebhook, gitlabWebhook http.Handler, queue jobs.JobQueue, llm LLMTester) *Server {
	if queue == nil {
		queue = nopQueue{}
	}
	return &Server{
		store:   st,
		cfg:     cfg,
		limiter: newLimiter(cfg.LoginMaxFails),
		github:  githubWebhook,
		gitlab:  gitlabWebhook,
		queue:   queue,
		llm:     llm,
	}
}

// Routes arma el mux con la superficie REST (mapa: rest_endpoints): F0 auth,
// webhook GitHub de F1 y settings/cola de F1. El encadenado es: logging →
// recover → (auth → csrf, endpoints de sesión; auth → csrf → admin, settings).
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", s.withLogging(s.withRecover(http.HandlerFunc(s.handleHealthz))))

	// Webhooks VCS: autenticados por firma (§9.3), sin cookie — fuera del
	// CSRF (§3.4). El handler valida, filtra y encola: jamás toca el gateway
	// LLM (§3.5).
	if s.github != nil {
		mux.Handle("POST /webhooks/github", s.withLogging(s.withRecover(s.github)))
	}
	if s.gitlab != nil {
		mux.Handle("POST /webhooks/gitlab", s.withLogging(s.withRecover(s.gitlab)))
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
