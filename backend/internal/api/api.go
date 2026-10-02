// Package api es el routing y middleware del dashboard (mapa: backend.api):
// ServeMux stdlib, handlers REST de F0 (auth por sesión, §3.4) y healthcheck.
// Los webhooks VCS (autenticados por firma HMAC, sin cookie) aterrizan en F1/F2
// y quedan fuera del CSRF (§3.4) — se montarán en este mismo mux.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Server agrupa las dependencias de los handlers HTTP.
type Server struct {
	store   *store.Store
	cfg     *config.Config
	limiter *limiter // backoff de login por usuario (§3.4), en memoria
}

// New arma el Server. El limiter de login vive en el Server (memoria del
// proceso — §9.6: filosofía single-worker, suficiente para 1-5 usuarios).
func New(st *store.Store, cfg *config.Config) *Server {
	return &Server{store: st, cfg: cfg, limiter: newLimiter(cfg.LoginMaxFails)}
}

// Routes arma el mux con la superficie F0. El encadenado es:
// logging → recover → (auth → csrf, solo endpoints de sesión).
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", s.withLogging(s.withRecover(http.HandlerFunc(s.handleHealthz))))

	// Login es mutante pero no autenticado por cookie: el CSRF de §3.4 no
	// aplica (la cookie SameSite=Lax + el rate limit son su defensa).
	mux.Handle("POST /api/auth/login", s.withLogging(s.withRecover(http.HandlerFunc(s.handleLogin))))

	// Logout y session requieren sesión vigente; logout es mutante → CSRF.
	mux.Handle("POST /api/auth/logout", s.withLogging(s.withRecover(s.withAuth(s.withCSRF(http.HandlerFunc(s.handleLogout))))))
	mux.Handle("GET /api/auth/session", s.withLogging(s.withRecover(s.withAuth(http.HandlerFunc(s.handleSession)))))

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
