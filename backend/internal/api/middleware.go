package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// sessionCookie es la cookie de sesión del dashboard (§3.4: HttpOnly +
// Secure + SameSite=Lax; en BD solo el hash del token).
const sessionCookie = "codeowl_session"

// csrfHeader es el header donde el dashboard reenvía el token CSRF.
const csrfHeader = "X-CSRF-Token"

// requestIDKey / authKey son claves de context.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	authKey
)

// authContext es lo que el middleware de auth deja en el request: la fila de
// sesión+usuario resuelta y el token plano de la cookie (necesario para
// derivar el CSRF y para el logout).
type authContext struct {
	Token string
	Row   store.GetSessionByTokenHashRow
}

// sessionOf devuelve el authContext del request; nil si no autenticó.
func sessionOf(r *http.Request) *authContext {
	auth, _ := r.Context().Value(authKey).(*authContext)
	return auth
}

// withRecover convierte panics en 500 JSON — un handler caído jamás tumba la
// api ni devuelve un stack trace al cliente (§9.9: el detalle va al log).
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic en handler", "method", r.Method, "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "error interno")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captura el status para el log de acceso.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// withLogging loguea cada request en JSON (§9.9): id, método, path, status y
// duración. Genera un request ID y lo propaga en la respuesta (desde F1
// viaja de webhook a job a llamada LLM).
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := requestID()
		w.Header().Set("X-Request-ID", reqID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, reqID))

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		slog.Info("http",
			"req_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duracion_ms", time.Since(start).Milliseconds(),
		)
	})
}

// requestID genera un identificador corto de request (64 bits aleatorios).
func requestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "sin-id" // sin entropía el sistema tiene problemas mayores; no bloquea el request
	}
	return hex.EncodeToString(b[:])
}

// withAuth carga la sesión desde la cookie al contexto. Sin cookie, expirada
// o revocada → 401 (§3.4: la sesión vigente se resuelve por el hash del token).
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "no autenticado")
			return
		}
		row, err := s.store.GetSessionByTokenHash(r.Context(), store.HashSessionToken(c.Value))
		if err != nil {
			// ErrNoRows (inexistente/expirada) y cualquier error de BD terminan
			// en 401: al cliente no hay nada que distinguir.
			writeError(w, http.StatusUnauthorized, "no autenticado")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), authKey, &authContext{Token: c.Value, Row: row}))
		next.ServeHTTP(w, r)
	})
}

// CSRF de §3.4 — mecanismo elegido: token derivado del token de sesión
// (SHA-256 con separación de dominio, csrfToken()), entregado al dashboard en
// las respuestas de login/session. Todo endpoint mutante autenticado por
// cookie debe reenviarlo en X-CSRF-Token; se compara en tiempo constante
// contra la derivación del token de la cookie. Es el "token de sesión
// verificado" del §3.4: el token CSRF filtrado no compromete la sesión (no es
// el token de sesión ni su hash) y un sitio cruzado no puede leerlo (la cookie
// es HttpOnly y el token solo lo conoce un JS del mismo origen).
//
// Los webhooks (F1/F2) se autentican por firma HMAC sin cookie y NO montan
// este middleware (§3.4).
func csrfToken(sessionToken string) string {
	sum := sha256.Sum256([]byte("codeowl-csrf:" + sessionToken))
	return hex.EncodeToString(sum[:])
}

// withCSRF verifica el token CSRF en todo método mutante (§3.4). Fallo → 403.
func (s *Server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete:
		default:
			next.ServeHTTP(w, r)
			return
		}
		auth := sessionOf(r)
		got := r.Header.Get(csrfHeader)
		want := csrfToken(auth.Token)
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeError(w, http.StatusForbidden, "token csrf inválido")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// decodeJSON decodifica y valida el cuerpo JSON del request en v. Cuerpos
// acotados: el login es la frontera de confianza del dashboard.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			writeError(w, http.StatusRequestEntityTooLarge, "cuerpo demasiado grande")
		case errors.Is(err, io.EOF):
			writeError(w, http.StatusBadRequest, "cuerpo json vacío")
		default:
			writeError(w, http.StatusBadRequest, "cuerpo json inválido")
		}
		return false
	}
	return true
}

// normalize recorta espacios y baja a minúsculas para comparar usuarios.
func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
