// Handlers de settings para proveedores LLM (mapa: rest_endpoints — F1, admin,
// guía §3.3/§3.4). La api_key NUNCA viaja en claro al dashboard: se cifra con
// la master key al guardar (§9.2) y se devuelve enmascarada al listar. El
// único punto donde la api toca internal/llm es la prueba de conexión (§3.5:
// una llamada mínima por rol vía gateway).
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// roleEmbedding: el rol cuyo habilitado dispara el guard de dims contra el
// índice (§3.3).
const roleEmbedding = "embedding"

// LLMTester es lo que la api usa de internal/llm: la prueba de conexión de
// settings (§3.5) y la sonda de embeddings que alimenta el guard de dims
// (§3.3: proveedor embedding enabled se valida contra el vector del índice
// antes de guardarse habilitado). *llm.Gateway lo satisface; los tests lo
// reemplazan.
type LLMTester interface {
	TestConnection(ctx context.Context, role, baseURL, apiKey, model string) error
	TestEmbedConnection(ctx context.Context, baseURL, apiKey, model string) (dims int, latencyMS int64, err error)
}

// providerRequest es el cuerpo de POST/PUT /api/providers.
type providerRequest struct {
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"` // en claro entra, cifrada vive
	Role     string `json:"role"`
	Priority int32  `json:"priority"`
	Enabled  bool   `json:"enabled"`
}

// providerView es la proyección al dashboard: api_key enmascarada, jamás el
// secreto (§9.9: prohibido exponer API keys).
type providerView struct {
	ID        int64     `json:"id"`
	BaseURL   string    `json:"base_url"`
	Model     string    `json:"model"`
	APIKey    string    `json:"api_key"` // enmascarada: "sk-***xyz"
	Role      string    `json:"role"`
	Priority  int32     `json:"priority"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func providerViewOf(p store.LlmProvider) providerView {
	return providerView{
		ID:        p.ID,
		BaseURL:   p.BaseUrl,
		Model:     p.Model,
		APIKey:    maskAPIKey(p.ApiKey),
		Role:      p.Role,
		Priority:  p.Priority,
		Enabled:   p.Enabled,
		CreatedAt: p.CreatedAt.Time,
		UpdatedAt: p.UpdatedAt.Time,
	}
}

// maskAPIKey deja el prefijo y las últimas 3: suficiente para que el admin
// reconozca la clave sin exponerla. Los ciphertext cortos van como "***".
func maskAPIKey(encoded string) string {
	if encoded == "" {
		return ""
	}
	if len(encoded) < 8 {
		return "***"
	}
	return encoded[:3] + "***" + encoded[len(encoded)-3:]
}

// validProviderRole: conjunto cerrado de roles (guía §3.3).
func validProviderRole(role string) bool {
	switch role {
	case "review", "cheap", "embedding":
		return true
	}
	return false
}

// validBaseURL: URL absoluta http(s) — un typo del admin no debe esperar al
// primer PR para descubrirse.
func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// handleListProviders: GET /api/providers.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.store.ListLlmProviders(r.Context())
	if err != nil {
		slog.Error("listando proveedores", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	views := make([]providerView, 0, len(providers))
	for _, p := range providers {
		views = append(views, providerViewOf(p))
	}
	writeJSON(w, http.StatusOK, views)
}

// handleCreateProvider: POST /api/providers. Valida, aplica el guard de dims
// del rol embedding (§3.3) y cifra la api_key antes de guardar (§9.2).
func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var req providerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateProvider(req, true); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	// Guard de dims (§3.3/§6 F4): habilitar un proveedor embedding exige
	// validar sus dims contra el vector del índice ANTES de guardarlo.
	if req.Role == roleEmbedding && req.Enabled {
		if status, msg := s.probeEmbeddingDims(r.Context(), req.BaseURL, req.APIKey, req.Model,
			r.Context().Value(requestIDKey)); msg != "" {
			writeError(w, status, msg)
			return
		}
	}
	encrypted, err := store.Encrypt(s.cfg.MasterKey, []byte(req.APIKey))
	if err != nil {
		slog.Error("cifrando api key", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	p, err := s.store.CreateLlmProvider(r.Context(), store.CreateLlmProviderParams{
		BaseUrl:  req.BaseURL,
		Model:    req.Model,
		ApiKey:   encrypted,
		Role:     req.Role,
		Priority: req.Priority,
		Enabled:  req.Enabled,
	})
	if err != nil {
		slog.Error("creando proveedor", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusCreated, providerViewOf(p))
}

// handleUpdateProvider: PUT /api/providers/{id}. api_key vacía = conservar la
// existente; no vacía = re-cifrar.
func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetLlmProvider(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "proveedor inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando proveedor", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	var req providerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// En update la api_key puede venir vacía (el dashboard no la re-muestra):
	// se conserva el ciphertext actual.
	if msg := validateProvider(req, req.APIKey != ""); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	encrypted := current.ApiKey
	if req.APIKey != "" {
		enc, err := store.Encrypt(s.cfg.MasterKey, []byte(req.APIKey))
		if err != nil {
			slog.Error("cifrando api key", "err", err, "req_id", r.Context().Value(requestIDKey))
			writeError(w, http.StatusInternalServerError, "error interno")
			return
		}
		encrypted = enc
	}
	// Guard de dims (§3.3/§6 F4): el probe habla con el proveedor real, así
	// que usa la key efectiva — la nueva o, si vino vacía, la guardada.
	if req.Role == roleEmbedding && req.Enabled {
		apiKey := req.APIKey
		if apiKey == "" {
			plain, err := store.Decrypt(s.cfg.MasterKey, current.ApiKey)
			if err != nil {
				slog.Error("descifrando api key", "err", err, "req_id", r.Context().Value(requestIDKey))
				writeError(w, http.StatusInternalServerError, "error interno")
				return
			}
			apiKey = string(plain)
		}
		if status, msg := s.probeEmbeddingDims(r.Context(), req.BaseURL, apiKey, req.Model,
			r.Context().Value(requestIDKey)); msg != "" {
			writeError(w, status, msg)
			return
		}
	}
	p, err := s.store.UpdateLlmProvider(r.Context(), store.UpdateLlmProviderParams{
		ID:       id,
		BaseUrl:  req.BaseURL,
		Model:    req.Model,
		ApiKey:   encrypted,
		Role:     req.Role,
		Priority: req.Priority,
		Enabled:  req.Enabled,
	})
	if err != nil {
		slog.Error("actualizando proveedor", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusOK, providerViewOf(p))
}

// handleDeleteProvider: DELETE /api/providers/{id}.
func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteLlmProvider(r.Context(), id); err != nil {
		slog.Error("borrando proveedor", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// testConnectionResponse: ok=false NO es error HTTP — el proveedor que no
// responde es un resultado de la prueba, no un fallo de la api.
type testConnectionResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// handleTestProvider: POST /api/providers/{id}/test — única puerta de la api
// a internal/llm (§3.5): descifra la key guardada y hace UNA llamada mínima
// por el rol del proveedor. Sin reintentos ni failover: eso lo hace el gateway.
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.store.GetLlmProvider(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "proveedor inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando proveedor", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	if s.llm == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway llm no configurado")
		return
	}
	apiKey, err := store.Decrypt(s.cfg.MasterKey, p.ApiKey)
	if err != nil {
		slog.Error("descifrando api key", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	start := time.Now()
	testErr := s.llm.TestConnection(r.Context(), p.Role, p.BaseUrl, string(apiKey), p.Model)
	latency := time.Since(start).Milliseconds()

	out := testConnectionResponse{OK: testErr == nil, LatencyMS: latency}
	if testErr != nil {
		// El detalle completo puede arrastrar la URL/key del proveedor: al
		// dashboard va el mensaje acotado (§9.9); el detalle queda en el log.
		slog.Warn("prueba de conexión falló", "provider_id", p.ID, "err", testErr)
		out.Error = "el proveedor no respondió a la llamada de prueba"
	}
	writeJSON(w, http.StatusOK, out)
}

// probeEmbeddingDims es el guard de dims del rol embedding (guía §3.3:
// "settings valida las dims del proveedor contra las del índice antes de
// habilitarlo"): UNA llamada de embeddings al proveedor (TestEmbedConnection,
// sin reintentos ni failover) contrastada contra el typmod del vector de
// repo_index leído del catálogo de PG — el DDL es la autoridad (siempre
// 1536, §3.3). Habilitar un proveedor embedding EXIGE una validación
// exitosa: la acción "probar conexión" existe justamente para verificarlo
// antes, así que un probe sin respuesta también rechaza con 422 (no es un
// error de la api: es la guardia de consistencia del índice — un mismatch
// haría fallar el INSERT de pgvector en el IndexJob). Devuelve (0, "") si
// pasa; si no, el status HTTP y el mensaje del rechazo.
func (s *Server) probeEmbeddingDims(ctx context.Context, baseURL, apiKey, model string, reqID any) (int, string) {
	if s.llm == nil {
		return http.StatusServiceUnavailable, "gateway llm no configurado"
	}
	dims, _, err := s.llm.TestEmbedConnection(ctx, baseURL, apiKey, model)
	if err != nil {
		slog.Warn("guard de dims: probe de embeddings falló", "err", err, "req_id", reqID)
		return http.StatusUnprocessableEntity, fmt.Sprintf(
			"no se pudieron validar las dims del proveedor (%v): habilitar un proveedor embedding exige una prueba de embeddings exitosa; usá la acción de probar conexión primero", err)
	}
	catalogo, err := s.store.GetRepoIndexDims(ctx)
	if err != nil {
		slog.Error("guard de dims: leyendo el catálogo", "err", err, "req_id", reqID)
		return http.StatusInternalServerError, "error interno"
	}
	if dims != int(catalogo) {
		return http.StatusUnprocessableEntity, fmt.Sprintf(
			"las dims del proveedor (%d) no coinciden con las del índice (%d): cambiar las dims requiere una migración nueva", dims, catalogo)
	}
	return 0, ""
}

// validateProvider valida el cuerpo de create/update; devuelve "" si es
// válido o el mensaje de error 400. requireKey distingue create (key
// obligatoria) de update (vacía = conservar).
func validateProvider(req providerRequest, requireKey bool) string {
	switch {
	case !validProviderRole(req.Role):
		return "role debe ser review, cheap o embedding"
	case !validBaseURL(req.BaseURL):
		return "base_url debe ser una URL http(s) absoluta"
	case strings.TrimSpace(req.Model) == "":
		return "model es obligatorio"
	case requireKey && strings.TrimSpace(req.APIKey) == "":
		return "api_key es obligatoria"
	}
	return ""
}

// pathID resuelve {id} del mux (Go 1.22+) como int64; 400 si no es numérico.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "id inválido")
		return 0, false
	}
	return id, true
}
