package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Tests de usuarios (guía §3.4): invitación con contraseña temporal de un
// solo uso, reset y deshabilitación con revocación de sesiones.

// inviteMember invita por la API y devuelve (username, temp_password).
func inviteMember(t *testing.T, e *testEnv) (string, string) {
	t.Helper()
	a := e.admin(t)
	username := fmt.Sprintf("usr-%d", time.Now().UnixNano())
	resp := e.do(t, http.MethodPost, "/api/users", a.cookie, a.csrf, map[string]string{"username": username})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("invitar debe ser 201, fue %d", resp.StatusCode)
	}
	var out struct {
		Username     string `json:"username"`
		TempPassword string `json:"temp_password"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Username, out.TempPassword
}

func TestInviteDevuelveTempPassword(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)

	username, temp := inviteMember(t, e)
	if len(temp) != 16 {
		t.Errorf("la contraseña temporal debe tener 16 chars, tiene %d (%q)", len(temp), temp)
	}
	// El listado la muestra como usuario member pendiente de cambio de clave,
	// jamás la contraseña.
	resp := e.do(t, http.MethodGet, "/api/users", a.cookie, a.csrf, nil)
	var users []userRow
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatal(err)
	}
	var visto *userRow
	for i := range users {
		if users[i].Username == username {
			visto = &users[i]
		}
	}
	if visto == nil || visto.Role != "member" || !visto.MustChangePassword || visto.Disabled {
		t.Errorf("el invitado debe listarse member+must_change_password activo: %+v", visto)
	}
	if strings.Contains(fmt.Sprint(users), temp) {
		t.Error("el listado jamás debe exponer la contraseña temporal")
	}
	// La temporal sirve para loguear.
	if got := e.loginAs(t, username, temp); got.cookie == nil {
		t.Error("el member debe poder loguear con su contraseña temporal")
	}
	// Username duplicado → 409.
	if got := e.do(t, http.MethodPost, "/api/users", a.cookie, a.csrf, map[string]string{"username": username}).StatusCode; got != http.StatusConflict {
		t.Errorf("username duplicado debe ser 409, fue %d", got)
	}
}

func TestResetPasswordRevocaSesiones(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)
	username, temp := inviteMember(t, e)
	m := e.loginAs(t, username, temp) // sesión que debe morir con el reset

	// ID del member: viene en el listado.
	resp := e.do(t, http.MethodGet, "/api/users", a.cookie, a.csrf, nil)
	var users []userRow
	_ = json.NewDecoder(resp.Body).Decode(&users)
	var id int64
	for _, u := range users {
		if u.Username == username {
			id = u.ID
		}
	}
	if id == 0 {
		t.Fatal("el member debe estar en el listado")
	}

	resp = e.do(t, http.MethodPut, fmt.Sprintf("/api/users/%d", id), a.cookie, a.csrf, map[string]string{"action": "reset_password"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset_password debe ser 200, fue %d", resp.StatusCode)
	}
	var out struct {
		TempPassword string `json:"temp_password"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.TempPassword == "" {
		t.Fatalf("reset debe devolver una temp_password nueva: %+v, err %v", out, err)
	}
	if out.TempPassword == temp {
		t.Error("la nueva contraseña temporal debe ser distinta de la anterior")
	}

	// La sesión vieja murió (§3.4: reset revoca sesiones).
	if got := e.do(t, http.MethodGet, "/api/auth/session", m.cookie, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("la sesión previa al reset debe quedar revocada (401), fue %d", got)
	}
	// La clave vieja ya no entra; la nueva sí.
	if got := e.login(t, username, temp).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("la temp_password vieja debe quedar invalidada (401), fue %d", got)
	}
	nueva := e.loginAs(t, username, out.TempPassword)
	if nueva.cookie == nil {
		t.Fatal("el member debe loguear con la nueva temp_password")
	}
	// ...y arranca con must_change_password=true.
	s := e.do(t, http.MethodGet, "/api/auth/session", nueva.cookie, "", nil)
	var sess authResponse
	_ = json.NewDecoder(s.Body).Decode(&sess)
	if !sess.User.MustChangePassword {
		t.Error("tras un reset el usuario debe arrancar con must_change_password=true")
	}
}

func TestDisableRevocaSesiones(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)
	username, temp := inviteMember(t, e)
	m := e.loginAs(t, username, temp)

	resp := e.do(t, http.MethodGet, "/api/users", a.cookie, a.csrf, nil)
	var users []userRow
	_ = json.NewDecoder(resp.Body).Decode(&users)
	var id int64
	for _, u := range users {
		if u.Username == username {
			id = u.ID
		}
	}

	if got := e.do(t, http.MethodPut, fmt.Sprintf("/api/users/%d", id), a.cookie, a.csrf, map[string]string{"action": "disable"}).StatusCode; got != http.StatusOK {
		t.Fatalf("disable debe ser 200, fue %d", got)
	}
	// La sesión activa murió (§3.4: la baja revoca acceso).
	if got := e.do(t, http.MethodGet, "/api/auth/session", m.cookie, "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("la sesión del deshabilitado debe quedar revocada (401), fue %d", got)
	}
	// Y aunque la clave sea correcta, no puede volver a entrar.
	if got := e.login(t, username, temp).StatusCode; got != http.StatusForbidden {
		t.Errorf("login de usuario deshabilitado debe ser 403, fue %d", got)
	}
	// Enable: vuelve a poder entrar.
	if got := e.do(t, http.MethodPut, fmt.Sprintf("/api/users/%d", id), a.cookie, a.csrf, map[string]string{"action": "enable"}).StatusCode; got != http.StatusOK {
		t.Fatalf("enable debe ser 200, fue %d", got)
	}
	if e.loginAs(t, username, temp).cookie == nil {
		t.Error("tras enable el usuario debe poder loguear")
	}
}

// Protección del último admin (§3.4): nadie puede deshabilitarse a sí mismo.
func TestNoPuedeDeshabilitarseASiMismo(t *testing.T) {
	e := newTestEnv(t, 5)
	a := e.admin(t)

	// ID del admin de la sesión.
	resp := e.do(t, http.MethodGet, "/api/auth/session", a.cookie, "", nil)
	var sess authResponse
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatal(err)
	}
	users := e.do(t, http.MethodGet, "/api/users", a.cookie, a.csrf, nil)
	var list []userRow
	_ = json.NewDecoder(users.Body).Decode(&list)
	var adminID int64
	for _, u := range list {
		if u.Username == e.user {
			adminID = u.ID
		}
	}
	if adminID == 0 {
		t.Fatal("el admin de pruebas debe estar en el listado")
	}

	if got := e.do(t, http.MethodPut, fmt.Sprintf("/api/users/%d", adminID), a.cookie, a.csrf, map[string]string{"action": "disable"}).StatusCode; got != http.StatusBadRequest {
		t.Errorf("auto-deshabilitarse debe ser 400, fue %d", got)
	}
	// Quedó intacto: la sesión sigue viva.
	if got := e.do(t, http.MethodGet, "/api/auth/session", a.cookie, "", nil).StatusCode; got != http.StatusOK {
		t.Errorf("el admin rechazado debe conservar su sesión (200), fue %d", got)
	}
	// Action desconocida → 400.
	if got := e.do(t, http.MethodPut, fmt.Sprintf("/api/users/%d", adminID), a.cookie, a.csrf, map[string]string{"action": "ascender-a-dios"}).StatusCode; got != http.StatusBadRequest {
		t.Errorf("action desconocida debe ser 400, fue %d", got)
	}
	_ = context.Background()
}
