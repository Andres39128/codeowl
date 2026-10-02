// Package llm es el gateway multi-proveedor OpenAI-compatible (mapa:
// backend.llm): routing por rol (review/cheap/embedding), failover por
// priority (§3.3), reintentos con backoff ante 429/5xx respetando Retry-After
// (§9.7) y topes de concurrencia global y por review (§9.6). Ningún otro
// paquete habla con proveedores LLM — todo pasa por acá; el gateway no sabe
// de findings, reviews ni PRs.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Store es el subconjunto de la store que consume el gateway (§4.5:
// interfaces donde se consumen). *store.Store la satisface.
type Store interface {
	ListEnabledLlmProvidersByRole(ctx context.Context, role string) ([]store.LlmProvider, error)
	CreateLlmUsage(ctx context.Context, arg store.CreateLlmUsageParams) (store.LlmUsage, error)
}

// Limits son los topes del gateway — todos config (§9.6/§9.7).
type Limits struct {
	MaxPerReview int           // llamadas LLM simultáneas por review (default 4)
	MaxGlobal    int           // llamadas LLM simultáneas del gateway (default 8)
	Timeout      time.Duration // timeout por llamada HTTP (default 120s)
	MaxRetries   int           // reintentos por proveedor ante 429/5xx (default 3)
}

const defaultBackoff = 500 * time.Millisecond

// Gateway enruta llamadas LLM por rol con failover por priority. Seguro para
// uso concurrente: los topes son semáforos de canales.
type Gateway struct {
	store     Store
	masterKey []byte
	limits    Limits
	http      *http.Client
	backoff   time.Duration // base del backoff exponencial (los tests la achican)

	global chan struct{}

	mu      sync.Mutex
	reviews map[string]*reviewSlots
}

// reviewSlots es el semáforo por review. held cuenta titulares (en espera o
// con slot): la entrada del mapa se borra cuando vuelve a 0 — sin fugas.
type reviewSlots struct {
	sem  chan struct{}
	held int
}

// New construye el gateway. No valida conectividad con proveedores: la cadena
// de failover se resuelve por llamada (§9.7). Toma los topes de config.Load,
// que ya los valida (≥1); acá se clampan igual para no colgarse nunca con un
// semáforo de capacidad 0.
func New(s Store, masterKey []byte, lim Limits) *Gateway {
	if lim.MaxPerReview < 1 {
		lim.MaxPerReview = 4
	}
	if lim.MaxGlobal < 1 {
		lim.MaxGlobal = 8
	}
	if lim.Timeout <= 0 {
		lim.Timeout = 120 * time.Second
	}
	if lim.MaxRetries < 0 {
		lim.MaxRetries = 3
	}
	return &Gateway{
		store:     s,
		masterKey: masterKey,
		limits:    lim,
		http:      http.DefaultClient,
		backoff:   defaultBackoff,
		global:    make(chan struct{}, lim.MaxGlobal),
		reviews:   map[string]*reviewSlots{},
	}
}

type reviewIDKey struct{}

// WithReview anota el contexto con la identidad de la corrida: con ella rige
// además el tope de concurrencia por review (§9.6, default 4). Sin anotación
// (ej. la prueba de conexión de settings) solo rige el tope global.
func WithReview(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, reviewIDKey{}, id)
}

// Complete resuelve los proveedores enabled del rol (orden priority, id —
// cadena de failover §3.3) y devuelve el primer contenido exitoso. Ante
// 429/5xx reintenta con backoff (Retry-After si existe, §9.7) y al agotar
// conmuta al siguiente proveedor; ante 400/401/403 conmuta sin reintentar.
// Si nadie responde, devuelve error — el caller marca la review partial
// (§9.6): nunca recorte silencioso. Cada éxito registra usage en llm_usage.
func (g *Gateway) Complete(ctx context.Context, role, systemPrompt, userPrompt string) (string, error) {
	providers, err := g.store.ListEnabledLlmProvidersByRole(ctx, role)
	if err != nil {
		return "", fmt.Errorf("listando proveedores del rol %s: %w", role, err)
	}
	if len(providers) == 0 {
		return "", fmt.Errorf("sin proveedores enabled para el rol %s (guarda de rol, §9.6)", role)
	}

	var errs []error
	for _, p := range providers {
		content, resp, err := g.completeProvider(ctx, p, systemPrompt, userPrompt)
		if err != nil {
			slog.Warn("llm: proveedor falló, conmutando al siguiente",
				"provider", p.BaseUrl, "rol", role, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", p.BaseUrl, err))
			continue
		}
		g.logUsage(ctx, role, p, resp.Usage)
		return content, nil
	}
	return "", fmt.Errorf("failover agotado para el rol %s (%d proveedores): %w",
		role, len(providers), errors.Join(errs...))
}

// TestConnection hace una llamada mínima para el botón "probar conexión" de
// settings: sin reintentos, sin failover y sin registro de usage (§3.3: la
// prueba de conexión no tiene job que la respalde — acá no se registra nada).
func (g *Gateway) TestConnection(ctx context.Context, role, baseURL, apiKey, model string) error {
	release, err := g.acquireGlobal(ctx)
	if err != nil {
		return err
	}
	defer release()

	callCtx, cancel := context.WithTimeout(ctx, g.limits.Timeout)
	defer cancel()
	if _, err := chatCompletion(callCtx, g.http, baseURL, apiKey, model,
		"responde únicamente: ok", "ping"); err != nil {
		return fmt.Errorf("prueba de conexión falló (rol %s, %s): %w", role, baseURL, err)
	}
	return nil
}

// completeProvider agota los reintentos de UN proveedor y devuelve la
// respuesta completa (para el usage) del primer intento exitoso.
func (g *Gateway) completeProvider(ctx context.Context, p store.LlmProvider, systemPrompt, userPrompt string) (string, chatResponse, error) {
	apiKey, err := store.Decrypt(g.masterKey, p.ApiKey)
	if err != nil {
		return "", chatResponse{}, fmt.Errorf("descifrando api_key del proveedor %s: %w", p.BaseUrl, err)
	}

	for attempt := 0; ; attempt++ {
		resp, retryable, wait, err := g.attempt(ctx, string(apiKey), p, systemPrompt, userPrompt)
		if err == nil {
			return resp.Choices[0].Message.Content, resp, nil
		}
		if !retryable || attempt >= g.limits.MaxRetries {
			return "", chatResponse{}, err
		}
		if wait == 0 {
			wait = g.backoff << attempt // exponencial: 500ms, 1s, 2s...
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return "", chatResponse{}, fmt.Errorf("esperando reintento: %w", ctx.Err())
		}
	}
}

// attempt ejecuta UN intento HTTP: toma los slots de concurrencia (global +
// por review si el ctx la anota — §9.6), aplica el timeout por llamada
// (§9.7) y clasifica el fallo: retryable ante 429/5xx y errores de
// transporte, con wait = Retry-After si el header existe.
func (g *Gateway) attempt(ctx context.Context, apiKey string, p store.LlmProvider, systemPrompt, userPrompt string) (chatResponse, bool, time.Duration, error) {
	relGlobal, err := g.acquireGlobal(ctx)
	if err != nil {
		return chatResponse{}, false, 0, err
	}
	defer relGlobal()
	relReview, err := g.acquireReview(ctx)
	if err != nil {
		return chatResponse{}, false, 0, err
	}
	defer relReview()

	callCtx, cancel := context.WithTimeout(ctx, g.limits.Timeout)
	defer cancel()

	resp, err := chatCompletion(callCtx, g.http, p.BaseUrl, apiKey, p.Model, systemPrompt, userPrompt)
	if err == nil {
		return resp, false, 0, nil
	}
	var se *statusError
	if errors.As(err, &se) {
		return chatResponse{}, se.retryable, se.retryAfter, err
	}
	// Fallo de transporte (red, timeout): reintentable.
	return chatResponse{}, true, 0, err
}

// logUsage registra la fila de llm_usage (§9.6: sin ese registro no hay
// métrica de costo en F5). Best-effort: un fallo de registro no invalida la
// respuesta ya obtenida. El identificador del proveedor es su base_url
// (llm_providers no tiene columna name).
func (g *Gateway) logUsage(ctx context.Context, role string, p store.LlmProvider, u usage) {
	if _, err := g.store.CreateLlmUsage(ctx, store.CreateLlmUsageParams{
		Role:      role,
		Provider:  p.BaseUrl,
		Model:     p.Model,
		TokensIn:  int32(u.PromptTokens),
		TokensOut: int32(u.CompletionTokens),
	}); err != nil {
		slog.Warn("llm: no se pudo registrar usage", "provider", p.BaseUrl, "error", err)
	}
}

func (g *Gateway) acquireGlobal(ctx context.Context) (func(), error) {
	select {
	case g.global <- struct{}{}:
		return func() { <-g.global }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("esperando slot global del gateway: %w", ctx.Err())
	}
}

// acquireReview toma slot del tope por review si el ctx la anota con
// WithReview; si no, es no-op. Ambos topes se toman siempre en el mismo
// orden (global → review): sin chance de deadlock.
func (g *Gateway) acquireReview(ctx context.Context) (func(), error) {
	id, _ := ctx.Value(reviewIDKey{}).(string)
	if id == "" {
		return func() {}, nil
	}

	g.mu.Lock()
	rs := g.reviews[id]
	if rs == nil {
		rs = &reviewSlots{sem: make(chan struct{}, g.limits.MaxPerReview)}
		g.reviews[id] = rs
	}
	rs.held++
	g.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			<-rs.sem
			g.releaseReview(id, rs)
		})
	}
	select {
	case rs.sem <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		g.releaseReview(id, rs)
		return nil, fmt.Errorf("esperando slot de la review %s: %w", id, ctx.Err())
	}
}

func (g *Gateway) releaseReview(id string, rs *reviewSlots) {
	g.mu.Lock()
	rs.held--
	if rs.held == 0 {
		delete(g.reviews, id)
	}
	g.mu.Unlock()
}
