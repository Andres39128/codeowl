package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// session es una sesión auxiliar (cookie + csrf) que no pisa la principal
// del testEnv: sirve para loguear al member junto al admin.
type session struct {
	cookie *http.Cookie
	csrf   string
}

// loginAs loguea user/pass sin tocar la sesión principal del env.
func (e *testEnv) loginAs(t *testing.T, user, pass string) session {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/api/auth/login", nil, "", credentials{Username: user, Password: pass})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login de %s debe ser 200, fue %d", user, resp.StatusCode)
	}
	var out authResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decodificando login: %v", err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return session{cookie: c, csrf: out.CSRFToken}
		}
	}
	t.Fatal("el login debe setear la cookie de sesión")
	return session{}
}

// admin loguea (o reusa) la sesión admin principal del env.
func (e *testEnv) admin(t *testing.T) session {
	t.Helper()
	if e.cookie == nil {
		e.login(t, e.user, e.pass)
	}
	return session{cookie: e.cookie, csrf: e.csrf}
}

// member crea e invita un usuario member por la API y devuelve su sesión.
// (Ejercita de paso POST /api/users.)
func (e *testEnv) member(t *testing.T) session {
	t.Helper()
	a := e.admin(t)
	username := fmt.Sprintf("member-%d", time.Now().UnixNano())
	resp := e.do(t, http.MethodPost, "/api/users", a.cookie, a.csrf, map[string]string{"username": username})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invitación de member debe ser 201, fue %d", resp.StatusCode)
	}
	var out struct {
		Username     string `json:"username"`
		TempPassword string `json:"temp_password"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.TempPassword == "" {
		t.Fatal("la invitación debe devolver temp_password")
	}
	return e.loginAs(t, username, out.TempPassword)
}

// createProvider crea un proveedor por la API y lo borra al terminar el test.
func (e *testEnv) createProvider(t *testing.T, role string, enabled bool) providerView {
	t.Helper()
	a := e.admin(t)
	resp := e.do(t, http.MethodPost, "/api/providers", a.cookie, a.csrf, providerRequest{
		BaseURL: "https://llm.example.com/v1",
		Model:   "test-model",
		APIKey:  "sk-clave-de-prueba-xyz",
		Role:    role,
		Enabled: enabled,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("crear proveedor debe ser 201, fue %d", resp.StatusCode)
	}
	var out providerView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.st.DeleteLlmProvider(context.Background(), out.ID) })
	return out
}

func TestProvidersCRUDRoundTrip(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)

	// Create: 201, la key vuelve enmascarada, no en claro.
	p := e.createProvider(t, "review", true)
	if got := p.APIKey; got == "sk-clave-de-prueba-xyz" || got == "" {
		t.Errorf("la api_key de la respuesta debe estar enmascarada, llegó %q", got)
	}

	// List: presente, enmascarada, sin rastro de la clave en claro.
	resp := e.do(t, http.MethodGet, "/api/providers", a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listar proveedores debe ser 200, fue %d", resp.StatusCode)
	}
	var list []providerView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, got := range list {
		if got.ID == p.ID {
			found = true
			if got.APIKey != p.APIKey {
				t.Errorf("la máscara debe ser estable en el listado: %q vs %q", got.APIKey, p.APIKey)
			}
		}
	}
	if !found {
		t.Error("el proveedor creado debe aparecer en el listado")
	}

	// Update con api_key vacía: conserva el ciphertext guardado.
	body := providerRequest{BaseURL: p.BaseURL, Model: p.Model, Role: p.Role, Priority: p.Priority, Enabled: false}
	resp = e.do(t, http.MethodPut, fmt.Sprintf("/api/providers/%d", p.ID), a.cookie, a.csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update sin api_key debe ser 200, fue %d", resp.StatusCode)
	}
	guardado, err := e.st.GetLlmProvider(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := store.Decrypt(make([]byte, 32), guardado.ApiKey); err != nil || string(plain) != "sk-clave-de-prueba-xyz" {
		t.Errorf("update sin api_key debe conservar la clave descifrable: %q, err %v", plain, err)
	}

	// Update con api_key nueva: re-cifra.
	body.APIKey = "sk-nueva-clave-987"
	resp = e.do(t, http.MethodPut, fmt.Sprintf("/api/providers/%d", p.ID), a.cookie, a.csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update con api_key debe ser 200, fue %d", resp.StatusCode)
	}
	recifrado, err := e.st.GetLlmProvider(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recifrado.ApiKey == guardado.ApiKey {
		t.Error("una api_key nueva debe re-cifrarse (ciphertext distinto)")
	}
	plain, err := store.Decrypt(make([]byte, 32), recifrado.ApiKey)
	if err != nil || string(plain) != "sk-nueva-clave-987" {
		t.Errorf("el nuevo ciphertext debe descifrar a la clave nueva: %q, err %v", plain, err)
	}

	// Delete.
	resp = e.do(t, http.MethodDelete, fmt.Sprintf("/api/providers/%d", p.ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete debe ser 200, fue %d", resp.StatusCode)
	}
	resp = e.do(t, http.MethodGet, "/api/providers", a.cookie, a.csrf, nil)
	var tras []providerView
	_ = json.NewDecoder(resp.Body).Decode(&tras)
	for _, got := range tras {
		if got.ID == p.ID {
			t.Error("el proveedor borrado no debe aparecer en el listado")
		}
	}
}

func TestProviderTestConnection(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)
	p := e.createProvider(t, "cheap", true)

	// Gateway OK → {ok:true, latency_ms}.
	resp := e.do(t, http.MethodPost, fmt.Sprintf("/api/providers/%d/test", p.ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test de conexión debe ser 200, fue %d", resp.StatusCode)
	}
	var out testConnectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Error != "" || out.LatencyMS < 0 {
		t.Errorf("con gateway sano la prueba debe ser ok:true sin error: %+v", out)
	}

	// Gateway que falla → 200 con ok:false y error acotado (no es un error
	// HTTP: es el RESULTADO de la prueba).
	e.llm.err = fmt.Errorf("503 del proveedor simulado")
	resp = e.do(t, http.MethodPost, fmt.Sprintf("/api/providers/%d/test", p.ID), a.cookie, a.csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("un proveedor caído es un resultado, no un error de la api: fue %d", resp.StatusCode)
	}
	out = testConnectionResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.OK || out.Error == "" {
		t.Errorf("con gateway en falla la prueba debe ser ok:false con error: %+v", out)
	}
}

func TestProviderCreateValidaciones(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)

	casos := []struct {
		nombre string
		req    providerRequest
	}{
		{"rol inválido", providerRequest{BaseURL: "https://x.example.com", Model: "m", APIKey: "sk-x", Role: "review-malo"}},
		{"base_url inválida", providerRequest{BaseURL: "no-es-una-url", Model: "m", APIKey: "sk-x", Role: "review"}},
		{"base_url sin host", providerRequest{BaseURL: "/api/relativa", Model: "m", APIKey: "sk-x", Role: "cheap"}},
		{"api_key vacía", providerRequest{BaseURL: "https://x.example.com", Model: "m", APIKey: "", Role: "embedding"}},
		{"model vacío", providerRequest{BaseURL: "https://x.example.com", Model: "", APIKey: "sk-x", Role: "review"}},
	}
	for _, tc := range casos {
		if got := e.do(t, http.MethodPost, "/api/providers", a.cookie, a.csrf, tc.req).StatusCode; got != http.StatusBadRequest {
			t.Errorf("%s: debe ser 400, fue %d", tc.nombre, got)
		}
	}
}

// Settings es del admin (§3.4): el member recibe 403 en toda la superficie
// de settings, con sesión válida (no es un problema de auth, es de rol).
func TestSettingsSoloAdmin(t *testing.T) {
	e := newTestEnv(t, 5)
	m := e.member(t)

	rutas := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/providers", nil},
		{http.MethodPost, "/api/providers", providerRequest{BaseURL: "https://x.com", Model: "m", APIKey: "sk-x", Role: "review"}},
		{http.MethodPut, "/api/providers/1", providerRequest{}},
		{http.MethodDelete, "/api/providers/1", nil},
		{http.MethodPost, "/api/providers/1/test", nil},
		{http.MethodGet, "/api/repos", nil},
		{http.MethodPost, "/api/repos", repoRequest{Vcs: "github", Owner: "o", Name: "n", ExternalID: 1}},
		{http.MethodPut, "/api/repos/1", repoUpdateRequest{}},
		{http.MethodDelete, "/api/repos/1", nil},
		{http.MethodGet, "/api/users", nil},
		{http.MethodPost, "/api/users", map[string]string{"username": "alguien"}},
		{http.MethodPut, "/api/users/1", map[string]string{"action": "enable"}},
	}
	for _, tc := range rutas {
		if got := e.do(t, tc.method, tc.path, m.cookie, m.csrf, tc.body).StatusCode; got != http.StatusForbidden {
			t.Errorf("%s %s como member debe ser 403, fue %d", tc.method, tc.path, got)
		}
	}
}
