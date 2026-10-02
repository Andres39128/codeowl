package api

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Backoff del rate limit de login (§3.4): tras N fallos consecutivos por
// usuario, cada fallo extra duplica la ventana (base 30s, tope 15m). Estado
// en memoria del proceso: §9.6 asume un solo proceso con usuarios reales —
// al reiniciar se limpia y un atacante distribuido es un problema de red
// (fail2ban/Caddy), no de la api. Documentado como límite de F0.
const (
	loginBackoffBase = 30 * time.Second
	loginBackoffMax  = 15 * time.Minute
)

// credentials es el cuerpo de POST /api/auth/login.
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// userView es la proyección del usuario que viaja al dashboard.
type userView struct {
	Username           string `json:"username"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password"`
}

// authResponse: usuario actual + token CSRF a usar en todo mutante (§3.4).
type authResponse struct {
	User      userView `json:"user"`
	CSRFToken string   `json:"csrf_token"`
}

// loginFails es el estado de backoff de un usuario.
type loginFails struct {
	count int
	until time.Time
}

// limiter aplica el backoff por usuario en memoria (§3.4: backoff tras N
// fallos; N viene de config.LOGIN_MAX_FAILS, default 5).
type limiter struct {
	mu      sync.Mutex
	fails   map[string]*loginFails
	maxRate int
}

func newLimiter(maxFails int) *limiter {
	return &limiter{fails: make(map[string]*loginFails), maxRate: maxFails}
}

// allow devuelve false si el usuario está en ventana de backoff, con el
// tiempo restante para el header Retry-After.
func (l *limiter) allow(username string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[username]
	if !ok || time.Now().After(f.until) {
		return true, 0
	}
	return false, time.Until(f.until)
}

// fail registra un fallo: pasa el N-ésimo, abre ventana de backoff que se
// duplica por fallo adicional (30s, 1m, 2m... tope 15m).
func (l *limiter) fail(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[username]
	if !ok {
		f = &loginFails{}
		l.fails[username] = f
	}
	f.count++
	if f.count >= l.maxRate {
		backoff := loginBackoffBase << uint(min(f.count-l.maxRate, 5)) // 30s << k, tope 15m
		f.until = time.Now().Add(min(backoff, loginBackoffMax))
	}
}

// reset limpia el estado del usuario tras un login exitoso.
func (l *limiter) reset(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, username)
}

// handleLogin: GetByUsername → CheckPassword → rechazo si Disabled →
// CreateSession con TTL de config → cookie de sesión (§3.4). Errores
// uniformes 401 para no enumerar usuarios; usuario deshabilitado → 403
// (lo deshabilitó el admin a propósito: merece saberlo en la UI).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var creds credentials
	if !decodeJSON(w, r, &creds) {
		return
	}
	creds.Username = normalize(creds.Username)
	if creds.Username == "" || creds.Password == "" {
		writeError(w, http.StatusBadRequest, "usuario y contraseña son obligatorios")
		return
	}

	if ok, wait := s.limiter.allow(creds.Username); !ok {
		w.Header().Set("Retry-After", wait.Truncate(time.Second).String())
		writeError(w, http.StatusTooManyRequests, "demasiados intentos fallidos; reintentá más tarde")
		return
	}

	user, err := s.store.GetByUsername(r.Context(), creds.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		s.limiter.fail(creds.Username)
		writeError(w, http.StatusUnauthorized, "credenciales inválidas")
		return
	}
	if err != nil {
		slog.Error("buscando usuario en login", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	if !store.CheckPassword(user.PasswordHash, creds.Password) {
		s.limiter.fail(creds.Username)
		writeError(w, http.StatusUnauthorized, "credenciales inválidas")
		return
	}
	if user.Disabled {
		writeError(w, http.StatusForbidden, "usuario deshabilitado")
		return
	}
	s.limiter.reset(creds.Username)

	token, err := store.NewSessionToken()
	if err != nil {
		slog.Error("generando token de sesión", "err", err)
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	if _, err := s.store.CreateSession(r.Context(), store.CreateSessionParams{
		TokenHash: store.HashSessionToken(token),
		UserID:    user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(s.cfg.SessionTTL), Valid: true},
	}); err != nil {
		slog.Error("creando sesión", "err", err)
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	http.SetCookie(w, s.sessionCookie(token, s.cfg.SessionTTL))
	writeJSON(w, http.StatusOK, authResponse{
		User:      userView{Username: user.Username, Role: user.Role, MustChangePassword: user.MustChangePassword},
		CSRFToken: csrfToken(token),
	})
}

// handleLogout revoca la sesión (§3.4: logout revoca) y vence la cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	auth := sessionOf(r)
	if err := s.store.DeleteSession(r.Context(), store.HashSessionToken(auth.Token)); err != nil {
		slog.Error("revocando sesión en logout", "err", err)
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	// Max-Age negativo: el browser borra la cookie (-1s para que el entero
	// de segundos quede en -1, el valor "borrar ya").
	http.SetCookie(w, s.sessionCookie("", -time.Second))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSession devuelve el usuario de la sesión actual o 401 (middleware).
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	auth := sessionOf(r)
	writeJSON(w, http.StatusOK, authResponse{
		User: userView{
			Username:           auth.Row.Username,
			Role:               auth.Row.Role,
			MustChangePassword: auth.Row.MustChangePassword,
		},
		CSRFToken: csrfToken(auth.Token),
	})
}

// sessionCookie arma la cookie de sesión con los atributos de §3.4. maxAge
// en segundos: TTL al loguear, -1 para vencerla al logout.
func (s *Server) sessionCookie(token string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge / time.Second),
		HttpOnly: true,
		Secure:   true, // Caddy termina TLS en prod; los browsers aceptan Secure en localhost
		SameSite: http.SameSiteLaxMode,
	}
}
