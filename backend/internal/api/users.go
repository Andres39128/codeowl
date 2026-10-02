// Handlers de usuarios (mapa: rest_endpoints — F1, admin, guía §3.4):
// invitación con contraseña temporal, reset de contraseña y deshabilitación.
// La contraseña temporal se muestra al admin UNA sola vez (viaja solo en esta
// respuesta); en BD vive su hash argon2id con must_change_password=true.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// userRow es la proyección de la lista de usuarios: jamás el hash.
type userRow struct {
	ID                 int64     `json:"id"`
	Username           string    `json:"username"`
	Role               string    `json:"role"`
	MustChangePassword bool      `json:"must_change_password"`
	Disabled           bool      `json:"disabled"`
	CreatedAt          time.Time `json:"created_at"`
}

// handleListUsers: GET /api/users.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		slog.Error("listando usuarios", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	out := make([]userRow, 0, len(users))
	for _, u := range users {
		out = append(out, userRow{
			ID:                 u.ID,
			Username:           u.Username,
			Role:               u.Role,
			MustChangePassword: u.MustChangePassword,
			Disabled:           u.Disabled,
			CreatedAt:          u.CreatedAt.Time,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleInviteUser: POST /api/users {username} — genera contraseña temporal
// de 16 chars, la guarda con argon2id + must_change_password=true y la
// devuelve UNA vez (§3.4). El rol de la invitación es member: los admins
// existen por seed del arranque (§3.4).
func (s *Server) handleInviteUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	username := normalize(req.Username)
	if username == "" {
		writeError(w, http.StatusBadRequest, "username es obligatorio")
		return
	}
	if _, err := s.store.GetByUsername(r.Context(), username); err == nil {
		writeError(w, http.StatusConflict, "ese username ya existe")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("buscando usuario duplicado", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	temp, err := tempPassword()
	if err != nil {
		slog.Error("generando contraseña temporal", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	hash, err := store.HashPassword(temp)
	if err != nil {
		slog.Error("hasheando contraseña temporal", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	if _, err := s.store.CreateUser(r.Context(), store.CreateUserParams{
		Username:           username,
		Role:               "member",
		PasswordHash:       hash,
		MustChangePassword: true,
	}); err != nil {
		slog.Error("creando usuario", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": username, "temp_password": temp})
}

// handleUserAction: PUT /api/users/{id} {action} — reset_password, disable,
// enable (§3.4). reset y disable revocan todas las sesiones del usuario.
func (s *Server) handleUserAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.store.GetUserByID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "usuario inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando usuario", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	switch req.Action {
	case "reset_password":
		temp, err := s.resetPassword(r, target.ID)
		if err != nil {
			slog.Error("reseteando contraseña", "err", err, "user_id", target.ID, "req_id", r.Context().Value(requestIDKey))
			writeError(w, http.StatusInternalServerError, "error interno")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"temp_password": temp})
	case "disable":
		if target.ID == sessionOf(r).Row.UserID {
			writeError(w, http.StatusBadRequest, "no podés deshabilitarte a vos mismo (protección del último admin, §3.4)")
			return
		}
		if err := s.setUserDisabled(r, target.ID, true); err != nil {
			slog.Error("deshabilitando usuario", "err", err, "user_id", target.ID, "req_id", r.Context().Value(requestIDKey))
			writeError(w, http.StatusInternalServerError, "error interno")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "enable":
		if err := s.setUserDisabled(r, target.ID, false); err != nil {
			slog.Error("habilitando usuario", "err", err, "user_id", target.ID, "req_id", r.Context().Value(requestIDKey))
			writeError(w, http.StatusInternalServerError, "error interno")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusBadRequest, "action debe ser reset_password, disable o enable")
	}
}

// resetPassword: hash nuevo + must_change_password + revocación de sesiones
// (§3.4: quien pierde su contraseña no conserva sesiones abiertas).
func (s *Server) resetPassword(r *http.Request, userID int64) (string, error) {
	temp, err := tempPassword()
	if err != nil {
		return "", fmt.Errorf("generando contraseña temporal: %w", err)
	}
	hash, err := store.HashPassword(temp)
	if err != nil {
		return "", fmt.Errorf("hasheando: %w", err)
	}
	if err := s.store.SetUserPassword(r.Context(), store.SetUserPasswordParams{
		ID: userID, PasswordHash: hash, MustChangePassword: true,
	}); err != nil {
		return "", err
	}
	if err := s.store.DeleteSessionsForUser(r.Context(), userID); err != nil {
		return "", err
	}
	return temp, nil
}

// setUserDisabled cambia el flag (la baja es un flag, §3.4) y revoca sesiones.
func (s *Server) setUserDisabled(r *http.Request, userID int64, disabled bool) error {
	if err := s.store.SetUserDisabled(r.Context(), store.SetUserDisabledParams{ID: userID, Disabled: disabled}); err != nil {
		return err
	}
	if !disabled {
		return nil // habilitar no toca sesiones: no tiene ninguna viva
	}
	return s.store.DeleteSessionsForUser(r.Context(), userID)
}

// tempPassword genera 16 chars hex de 8 bytes aleatorios (§3.4). Hex y no
// base64: la va a tipear un humano desde el panel del admin.
func tempPassword() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
