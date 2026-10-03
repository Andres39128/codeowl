package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Tests del gateway contra un servidor stub OpenAI-compatible (mapa:
// backend.llm — llm_test.go). Sin BD: la store se reemplaza por un fake de
// dos métodos (la interfaz Store se define donde se consume, §4.5).

// fakeStore graba los usages registrados y devuelve la cadena de failover.
type fakeStore struct {
	mu        sync.Mutex
	providers []store.LlmProvider
	usages    []store.CreateLlmUsageParams
}

func (f *fakeStore) ListEnabledLlmProvidersByRole(_ context.Context, role string) ([]store.LlmProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.LlmProvider
	for _, p := range f.providers {
		if p.Role == role && p.Enabled {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateLlmUsage(_ context.Context, arg store.CreateLlmUsageParams) (store.LlmUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usages = append(f.usages, arg)
	return store.LlmUsage{ID: int64(len(f.usages))}, nil
}

func (f *fakeStore) recordedUsages() []store.CreateLlmUsageParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.CreateLlmUsageParams(nil), f.usages...)
}

// masterKeyTest es una clave AES-256 válida para cifrar las api_keys del stub.
var masterKeyTest = make([]byte, 32)

// newStub arma el servidor stub que sirve chat completions.
func newStub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// stubOK responde 200 con el contenido y usage dados.
func stubOK(content string, tokensIn, tokensOut int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
			content, tokensIn, tokensOut)
	}
}

// provider arma una fila llm_provider apuntando al stub, con api_key cifrada.
func provider(t *testing.T, baseURL, role string, priority int32) store.LlmProvider {
	t.Helper()
	enc, err := store.Encrypt(masterKeyTest, []byte("sk-stub"))
	if err != nil {
		t.Fatal(err)
	}
	return store.LlmProvider{
		ID: int64(priority), BaseUrl: baseURL, Model: "stub-model",
		ApiKey: enc, Role: role, Priority: priority, Enabled: true,
	}
}

// newGateway arma el gateway con backoff mínimo para tests veloces.
func newGateway(t *testing.T, s Store, lim Limits) *Gateway {
	t.Helper()
	g := New(s, masterKeyTest, lim)
	g.backoff = time.Millisecond
	return g
}

// testLimits son topes chicos por defecto para los tests.
func testLimits() Limits {
	return Limits{MaxPerReview: 2, MaxGlobal: 2, Timeout: 2 * time.Second, MaxRetries: 3}
}

func TestCompleteExitosoRegistraUsage(t *testing.T) {
	var mu sync.Mutex
	var auth, path string
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		mu.Unlock()
		stubOK("resumen del diff", 11, 7)(w, r)
	})

	fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "review", 1)}}
	g := newGateway(t, fs, testLimits())

	out, err := g.Complete(context.Background(), "review", "sys", "user")
	if err != nil {
		t.Fatalf("Complete(): %v", err)
	}
	if out != "resumen del diff" {
		t.Errorf("content: got %q want %q", out, "resumen del diff")
	}
	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer sk-stub" {
		t.Errorf("la api_key descifrada no viajó en el Bearer: %q", auth)
	}
	if path != "/v1/chat/completions" {
		t.Errorf("path: got %q want /v1/chat/completions", path)
	}

	usages := fs.recordedUsages()
	if len(usages) != 1 {
		t.Fatalf("usage registrado: got %d filas want 1", len(usages))
	}
	u := usages[0]
	if u.Role != "review" || u.Provider != srv.URL || u.Model != "stub-model" ||
		u.TokensIn != 11 || u.TokensOut != 7 {
		t.Errorf("usage mal registrado: %+v", u)
	}
	if u.JobID.Valid {
		t.Error("job_id debe ser null desde Complete sin contexto de job")
	}
}

func TestFailoverAlSiguienteProveedor(t *testing.T) {
	var hits1 int
	srv1 := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		mu := &sync.Mutex{}
		mu.Lock()
		hits1++
		mu.Unlock()
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
	})
	srv2 := newStub(t, stubOK("del segundo", 3, 2))

	fs := &fakeStore{providers: []store.LlmProvider{
		provider(t, srv1.URL, "review", 1), // prioridad 1: falla siempre
		provider(t, srv2.URL, "review", 2), // prioridad 2: success
	}}
	g := newGateway(t, fs, testLimits())

	out, err := g.Complete(context.Background(), "review", "sys", "user")
	if err != nil {
		t.Fatalf("Complete(): %v", err)
	}
	if out != "del segundo" {
		t.Errorf("content: got %q want el del segundo proveedor", out)
	}
	if hits1 < 1 {
		t.Error("el primer proveedor nunca fue intentado")
	}
	usages := fs.recordedUsages()
	if len(usages) != 1 || usages[0].Provider != srv2.URL {
		t.Errorf("usage debe registrar solo el proveedor exitoso: %+v", usages)
	}
}

func TestRateLimitRespetaRetryAfter(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0") // válido: reintento inmediato
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		stubOK("tras el 429", 1, 1)(w, nil)
	})

	fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "review", 1)}}
	g := newGateway(t, fs, testLimits())

	out, err := g.Complete(context.Background(), "review", "sys", "user")
	if err != nil {
		t.Fatalf("Complete(): %v", err)
	}
	if out != "tras el 429" {
		t.Errorf("content: got %q", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("llamadas: got %d want 2 (un 429 y un reintento)", calls)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"5", 5 * time.Second},
		{"0", 0},               // inválido para backoff → cae al exponencial
		{"", 0},                // ausente
		{"abc", 0},             // forma no soportada (HTTP-date) → exponencial
		{"-3", 0},              // negativo
		{"120", maxRetryAfter}, // acotado: un valor patológico no retiene slots
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.in); got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestErroresClienteSinReintento(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			srv := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				http.Error(w, "client error", code)
			})
			fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "review", 1)}}
			g := newGateway(t, fs, testLimits())

			if _, err := g.Complete(context.Background(), "review", "sys", "user"); err == nil {
				t.Fatal("esperaba error")
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 1 {
				t.Errorf("los errores del cliente no reintentan: %d llamadas", calls)
			}
		})
	}
}

func TestFailoverAgotadoDevuelveError(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	fs := &fakeStore{providers: []store.LlmProvider{
		provider(t, srv.URL, "review", 1),
		provider(t, srv.URL, "review", 2),
	}}
	lim := testLimits()
	lim.MaxRetries = 1 // por proveedor: 1 intento + 1 reintento
	g := newGateway(t, fs, lim)

	_, err := g.Complete(context.Background(), "review", "sys", "user")
	if err == nil || !strings.Contains(err.Error(), "failover agotado") {
		t.Errorf("el error debe declarar failover agotado (§9.6): %v", err)
	}
	if usages := fs.recordedUsages(); len(usages) != 0 {
		t.Errorf("sin éxito no se registra usage: %+v", usages)
	}
}

func TestSinProveedoresHabilitados(t *testing.T) {
	fs := &fakeStore{}
	g := newGateway(t, fs, testLimits())
	_, err := g.Complete(context.Background(), "review", "sys", "user")
	if err == nil || !strings.Contains(err.Error(), "sin proveedores enabled") {
		t.Errorf("esperaba error de guarda de rol: %v", err)
	}
}

// trackingStub mide el máximo de llamadas en vuelo.
type trackingStub struct {
	server   *httptest.Server
	inFlight int
	maxSeen  int
	mu       sync.Mutex
}

func newTrackingStub(t *testing.T, delay time.Duration) *trackingStub {
	ts := &trackingStub{}
	ts.server = newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		ts.mu.Lock()
		ts.inFlight++
		if ts.inFlight > ts.maxSeen {
			ts.maxSeen = ts.inFlight
		}
		ts.mu.Unlock()
		time.Sleep(delay)
		ts.mu.Lock()
		ts.inFlight--
		ts.mu.Unlock()
		stubOK("ok", 1, 1)(w, nil)
	})
	return ts
}

func (ts *trackingStub) maxInFlight() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.maxSeen
}

func TestTopeGlobalDeConcurrencia(t *testing.T) {
	stub := newTrackingStub(t, 30*time.Millisecond)
	fs := &fakeStore{providers: []store.LlmProvider{provider(t, stub.server.URL, "review", 1)}}
	lim := testLimits()
	lim.MaxGlobal = 2
	g := newGateway(t, fs, lim)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Complete(context.Background(), "review", "sys", "user"); err != nil {
				t.Errorf("Complete(): %v", err)
			}
		}()
	}
	wg.Wait()
	if max := stub.maxInFlight(); max > 2 {
		t.Errorf("tope global violado: %d llamadas simultáneas (tope 2)", max)
	} else if max < 2 {
		t.Errorf("no hubo paralelismo: máximo %d en vuelo", max)
	}
}

func TestTopePorReview(t *testing.T) {
	stub := newTrackingStub(t, 40*time.Millisecond)
	fs := &fakeStore{providers: []store.LlmProvider{provider(t, stub.server.URL, "review", 1)}}
	lim := testLimits()
	lim.MaxGlobal = 8
	lim.MaxPerReview = 1
	g := newGateway(t, fs, lim)

	ctx := WithReview(context.Background(), "corrida-1")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Complete(ctx, "review", "sys", "user"); err != nil {
				t.Errorf("Complete(): %v", err)
			}
		}()
	}
	wg.Wait()
	if max := stub.maxInFlight(); max != 1 {
		t.Errorf("tope por review violado: %d simultáneas (tope 1)", max)
	}
}

func TestTestConnection(t *testing.T) {
	var gotReq chatRequest
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		stubOK("ok", 1, 1)(w, r)
	})
	fs := &fakeStore{}
	g := newGateway(t, fs, testLimits())

	if err := g.TestConnection(context.Background(), "review", srv.URL, "sk-plano", "stub-model"); err != nil {
		t.Fatalf("TestConnection(): %v", err)
	}
	if gotReq.Model != "stub-model" {
		t.Errorf("model: got %q", gotReq.Model)
	}
	if usages := fs.recordedUsages(); len(usages) != 0 {
		t.Errorf("la prueba de conexión no registra usage: %+v", usages)
	}

	bad := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	if err := g.TestConnection(context.Background(), "review", bad.URL, "sk-mala", "stub-model"); err == nil {
		t.Error("esperaba error con 401")
	}
}

// ---- Embeddings (F4, §6) ----

// embeddingsPayload arma la respuesta del stub: un embedding por input, el
// vector del índice i vale i+1 en todas sus dims (verifica el rearmado por
// index). order simula respuestas desordenadas (nil = orden natural);
// prompt_tokens es 3 por input (verifica la acumulación entre lotes).
func embeddingsPayload(n, dims int, order []int) map[string]any {
	idxs := order
	if idxs == nil {
		idxs = make([]int, n)
		for i := range idxs {
			idxs[i] = i
		}
	}
	data := make([]map[string]any, 0, len(idxs))
	for _, idx := range idxs {
		vec := make([]float64, dims)
		for i := range vec {
			vec[i] = float64(idx + 1)
		}
		data = append(data, map[string]any{"embedding": vec, "index": idx})
	}
	return map[string]any{
		"data":  data,
		"usage": map[string]int{"prompt_tokens": 3 * n},
	}
}

// stubEmbeddingsOK responde 200 con embeddingsPayload para el input recibido.
func stubEmbeddingsOK(dims int, order []int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embeddingsPayload(len(req.Input), dims, order))
	}
}

func TestEmbedRearmaPorIndex(t *testing.T) {
	var mu sync.Mutex
	var auth, path string
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		mu.Unlock()
		stubEmbeddingsOK(4, []int{2, 0, 1})(w, r) // desordenado a propósito
	})
	fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "embedding", 1)}}
	g := newGateway(t, fs, testLimits())

	vecs, err := g.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed(): %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("vectores: got %d want 3", len(vecs))
	}
	for i, vec := range vecs {
		if len(vec) != 4 || vec[0] != float32(i+1) {
			t.Errorf("vector %d mal rearmado: dims=%d v[0]=%v want dims=4 v[0]=%d", i, len(vec), vec[0], i+1)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer sk-stub" {
		t.Errorf("la api_key descifrada no viajó en el Bearer: %q", auth)
	}
	if path != "/v1/embeddings" {
		t.Errorf("path: got %q want /v1/embeddings", path)
	}

	usages := fs.recordedUsages()
	if len(usages) != 1 {
		t.Fatalf("usage: got %d filas want 1", len(usages))
	}
	u := usages[0]
	if u.Role != "embedding" || u.Provider != srv.URL || u.TokensIn != 9 || u.TokensOut != 0 {
		t.Errorf("usage mal registrado: %+v", u)
	}
}

func TestEmbedDivideEnLotes(t *testing.T) {
	var mu sync.Mutex
	var lens []int
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		lens = append(lens, len(req.Input))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embeddingsPayload(len(req.Input), 2, nil))
	})
	fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "embedding", 1)}}
	lim := testLimits()
	lim.EmbedBatchSize = 2
	g := newGateway(t, fs, lim)

	vecs, err := g.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed(): %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lens) != 2 || lens[0] != 2 || lens[1] != 1 {
		t.Errorf("lotes: got %v want [2 1]", lens)
	}
	if len(vecs) != 3 {
		t.Errorf("vectores concatenados: got %d want 3", len(vecs))
	}
	if usages := fs.recordedUsages(); len(usages) != 1 || usages[0].TokensIn != 9 {
		t.Errorf("usage debe acumular los tokens de ambos lotes: %+v", usages)
	}
}

func TestEmbedDimsInconsistentesFalla(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[1,2],"index":0},{"embedding":[1,2,3],"index":1}]}`)
	})
	fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "embedding", 1)}}
	g := newGateway(t, fs, testLimits())

	if _, err := g.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("esperaba error por dims inconsistentes")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("una respuesta malformada no se reintenta: %d llamadas", calls)
	}
	if usages := fs.recordedUsages(); len(usages) != 0 {
		t.Errorf("sin éxito no se registra usage: %+v", usages)
	}
}

func TestEmbedMalformadoConmutaAlSiguiente(t *testing.T) {
	var mu sync.Mutex
	var calls1 int
	srv1 := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls1++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `esto no es json`)
	})
	srv2 := newStub(t, stubEmbeddingsOK(3, nil))
	fs := &fakeStore{providers: []store.LlmProvider{
		provider(t, srv1.URL, "embedding", 1),
		provider(t, srv2.URL, "embedding", 2),
	}}
	g := newGateway(t, fs, testLimits())

	vecs, err := g.Embed(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("Embed(): %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 || vecs[0][0] != 1 {
		t.Errorf("vector del segundo proveedor: %v", vecs)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls1 != 1 {
		t.Errorf("una respuesta malformada no se reintenta: %d llamadas", calls1)
	}
	if usages := fs.recordedUsages(); len(usages) != 1 || usages[0].Provider != srv2.URL {
		t.Errorf("usage debe registrar solo el proveedor exitoso: %+v", usages)
	}
}

func TestEmbedSinProveedoresHabilitados(t *testing.T) {
	fs := &fakeStore{}
	g := newGateway(t, fs, testLimits())
	_, err := g.Embed(context.Background(), []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "sin proveedores enabled") {
		t.Errorf("esperaba error de guarda de rol: %v", err)
	}
}

func TestEmbedVacioNoLlama(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		stubEmbeddingsOK(2, nil)(w, r)
	})
	fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "embedding", 1)}}
	g := newGateway(t, fs, testLimits())

	for _, in := range [][]string{nil, {}} {
		vecs, err := g.Embed(context.Background(), in)
		if err != nil {
			t.Fatalf("Embed(%v): %v", in, err)
		}
		if len(vecs) != 0 {
			t.Errorf("Embed(%v): got %d vectores want 0", in, len(vecs))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("sin input no hay llamada HTTP: %d", calls)
	}
}

func TestEmbedRateLimitRespetaRetryAfter(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0") // válido: reintento inmediato
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		stubEmbeddingsOK(2, nil)(w, r)
	})
	fs := &fakeStore{providers: []store.LlmProvider{provider(t, srv.URL, "embedding", 1)}}
	g := newGateway(t, fs, testLimits())

	vecs, err := g.Embed(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("Embed(): %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("vectores: got %d want 1", len(vecs))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("llamadas: got %d want 2 (un 429 y un reintento)", calls)
	}
}

func TestTestEmbedConnection(t *testing.T) {
	srv := newStub(t, stubEmbeddingsOK(8, nil))
	fs := &fakeStore{}
	g := newGateway(t, fs, testLimits())

	dims, latency, err := g.TestEmbedConnection(context.Background(), srv.URL, "sk-plano", "stub-model")
	if err != nil {
		t.Fatalf("TestEmbedConnection(): %v", err)
	}
	if dims != 8 {
		t.Errorf("dims: got %d want 8", dims)
	}
	if latency < 0 {
		t.Errorf("latencia negativa: %d", latency)
	}
	if usages := fs.recordedUsages(); len(usages) != 0 {
		t.Errorf("la sonda de embeddings no registra usage: %+v", usages)
	}

	bad := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	if _, _, err := g.TestEmbedConnection(context.Background(), bad.URL, "sk-mala", "stub-model"); err == nil {
		t.Error("esperaba error con 401")
	}
}

func TestTestConnectionEmbeddingUsaEmbeddings(t *testing.T) {
	var mu sync.Mutex
	var path string
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		mu.Unlock()
		stubEmbeddingsOK(4, nil)(w, r)
	})
	fs := &fakeStore{}
	g := newGateway(t, fs, testLimits())

	if err := g.TestConnection(context.Background(), "embedding", srv.URL, "sk-plano", "stub-model"); err != nil {
		t.Fatalf("TestConnection(rol embedding): %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if path != "/v1/embeddings" {
		t.Errorf("el rol embedding no debe hablar chat: %s", path)
	}
}
