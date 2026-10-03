// Handlers de repos conectados (mapa: rest_endpoints — F1, admin, guía §3.3).
// Conectar = registrar (owner/name/external_id); la desconexión es el flag
// enabled, jamás un delete (§3.5 — los findings y reviews mantienen su FK).
// Guarda de rol (§9.6): conectar o re-habilitar exige al menos un proveedor
// LLM enabled en el rol review — sin ese guard, cada PR del repo quemaría una
// corrida entera de reintentos hasta failed. La reconexión encola un
// ReconcileJob (§3.5): la api solo encola, jamás lo ejecuta.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// errNoReviewProvider es el motivo del 422 de la guarda de rol (§9.6).
var errNoReviewProvider = errors.New("sin proveedor review enabled")

// repoRequest es el cuerpo de POST /api/repos. Los campos de GitLab son
// opcionales (data-entry, guía §3.3/§6 F2): llegan en claro y se cifran antes
// de guardar (§9.2). En GitHub los secretos son globales de la App (§9.3).
type repoRequest struct {
	Vcs           string `json:"vcs"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	ExternalID    int64  `json:"external_id"`    // ID numérico del repo en el VCS
	WebhookSecret string `json:"webhook_secret"` // GitLab: signing token del proyecto
	APIToken      string `json:"api_token"`      // GitLab: project access token (scope api)
	DeployKey     string `json:"deploy_key"`     // GitLab: deploy key privada de clonado
	BaseURL       string `json:"base_url"`       // GitLab self-managed; vacío = gitlab.com
}

// repoView es la proyección al dashboard: sin secretos — ni siquiera
// enmascarados (el dashboard no los re-muestra; actualizar = re-enviar).
type repoView struct {
	ID           int64  `json:"id"`
	Vcs          string `json:"vcs"`
	ExternalID   int64  `json:"external_id"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	ReviewDrafts bool   `json:"review_drafts"`
	Language     string `json:"language"`
	ChatOrgOnly  bool   `json:"chat_org_only"`
}

func repoViewOf(r store.Repository) repoView {
	return repoView{
		ID:           r.ID,
		Vcs:          r.Vcs,
		ExternalID:   r.ExternalID,
		Owner:        r.Owner,
		Name:         r.Name,
		Enabled:      r.Enabled,
		ReviewDrafts: r.ReviewDrafts,
		Language:     r.Language,
		ChatOrgOnly:  r.ChatOrgOnly,
	}
}

// encryptedText opcional: texto vacío → NULL; texto presente → cifrado (§9.2).
func (s *Server) encryptedText(v string) (pgtype.Text, error) {
	if v == "" {
		return pgtype.Text{}, nil
	}
	enc, err := store.Encrypt(s.cfg.MasterKey, []byte(v))
	if err != nil {
		return pgtype.Text{}, err
	}
	return pgtype.Text{String: enc, Valid: true}, nil
}

// handleListRepos: GET /api/repos.
func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepositories(r.Context())
	if err != nil {
		slog.Error("listando repos", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	views := make([]repoView, 0, len(repos))
	for _, repo := range repos {
		views = append(views, repoViewOf(repo))
	}
	writeJSON(w, http.StatusOK, views)
}

// handleCreateRepo: POST /api/repos — conectar un repo. Guarda de rol §9.6
// antes de escribir: sin proveedor review enabled → 422 con mensaje claro.
func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	var req repoRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch {
	case req.Vcs != "github" && req.Vcs != "gitlab":
		writeError(w, http.StatusBadRequest, "vcs debe ser github o gitlab")
		return
	case req.Owner == "" || req.Name == "":
		writeError(w, http.StatusBadRequest, "owner y name son obligatorios")
		return
	case req.ExternalID < 1:
		writeError(w, http.StatusBadRequest, "external_id debe ser el ID numérico del repo en el VCS")
		return
	}
	if err := s.requireReviewProvider(r); err != nil {
		writeGuardRejection(w, err)
		return
	}
	// Clave natural (vcs, external_id): el webhook resuelve el repo por ella —
	// un duplicado es 409, no un error 500 del constraint.
	if _, err := s.store.GetRepositoryByVCSExternalID(r.Context(), store.GetRepositoryByVCSExternalIDParams{
		Vcs: req.Vcs, ExternalID: req.ExternalID,
	}); err == nil {
		writeError(w, http.StatusConflict, "ese repo ya está conectado")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("buscando repo duplicado", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}

	arg, err := s.createRepoParams(r, req)
	if err != nil {
		slog.Error("cifrando secretos del repo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	repo, err := s.store.CreateRepository(r.Context(), arg)
	if err != nil {
		slog.Error("conectando repo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	s.enqueueIndex(r, repo.ID) // §6 F4: el repo conectado arranca con su índice (si hay rol embedding)
	writeJSON(w, http.StatusCreated, repoViewOf(repo))
}

// createRepoParams arma los params de CreateRepository, cifrando los secretos
// GitLab (§9.2): webhook_secret, api_token y deploy_key. base_url es la URL
// self-managed (no es secreta, va en claro); en GitHub no se guardan secretos
// por repo (App global, §9.3).
func (s *Server) createRepoParams(r *http.Request, req repoRequest) (store.CreateRepositoryParams, error) {
	arg := store.CreateRepositoryParams{
		Vcs:        req.Vcs,
		ExternalID: req.ExternalID,
		Owner:      req.Owner,
		Name:       req.Name,
		BaseUrl:    pgtype.Text{String: req.BaseURL, Valid: req.BaseURL != ""},
	}
	if req.Vcs != "gitlab" {
		return arg, nil
	}
	var err error
	if arg.WebhookSecret, err = s.encryptedText(req.WebhookSecret); err != nil {
		return arg, err
	}
	if arg.ApiToken, err = s.encryptedText(req.APIToken); err != nil {
		return arg, err
	}
	if arg.DeployKey, err = s.encryptedText(req.DeployKey); err != nil {
		return arg, err
	}
	return arg, nil
}

// handleUpdateRepo: PUT /api/repos/{id} — flag enabled + config por repo.
// Re-habilitar un repo deshabilitado vuelve a pasar la guarda de rol (§9.6) y
// encola el ReconcileJob de reconexión (§3.5).
type repoUpdateRequest struct {
	Enabled      *bool  `json:"enabled"`
	Language     string `json:"language"`
	ReviewDrafts *bool  `json:"review_drafts"`
	ChatOrgOnly  *bool  `json:"chat_org_only"`
}

func (s *Server) handleUpdateRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetRepository(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "repo inexistente")
		return
	}
	if err != nil {
		slog.Error("buscando repo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	var req repoUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	// Guarda de rol en la transición deshabilitado → habilitado (§9.6):
	// ANTES de escribir — el repo queda como estaba si falta el proveedor.
	enabled := current.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
		if enabled && !current.Enabled {
			if err := s.requireReviewProvider(r); err != nil {
				writeGuardRejection(w, err)
				return
			}
		}
	}

	// Config: campo ausente o vacío conserva el valor actual.
	language := current.Language
	if req.Language != "" {
		language = req.Language
	}
	reviewDrafts := current.ReviewDrafts
	if req.ReviewDrafts != nil {
		reviewDrafts = *req.ReviewDrafts
	}
	chatOrgOnly := current.ChatOrgOnly
	if req.ChatOrgOnly != nil {
		chatOrgOnly = *req.ChatOrgOnly
	}
	repo, err := s.store.UpdateRepository(r.Context(), store.UpdateRepositoryParams{
		ID:            id,
		Owner:         current.Owner,
		Name:          current.Name,
		WebhookSecret: current.WebhookSecret,
		SecretToken:   current.SecretToken,
		ApiToken:      current.ApiToken,
		BaseUrl:       current.BaseUrl,
		DeployKey:     current.DeployKey,
		ReviewDrafts:  reviewDrafts,
		Language:      language,
		ChatOrgOnly:   chatOrgOnly,
	})
	if err != nil {
		slog.Error("actualizando repo", "err", err, "req_id", r.Context().Value(requestIDKey))
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	if enabled != repo.Enabled {
		if repo, err = s.store.SetRepositoryEnabled(r.Context(), store.SetRepositoryEnabledParams{ID: id, Enabled: enabled}); err != nil {
			slog.Error("cambiando flag enabled del repo", "err", err, "req_id", r.Context().Value(requestIDKey))
			writeError(w, http.StatusInternalServerError, "error interno")
			return
		}
	}
	if enabled && !current.Enabled {
		s.enqueueReconcile(r, id)
		s.enqueueIndex(r, id)
	}
	writeJSON(w, http.StatusOK, repoViewOf(repo))
}

// enqueueReconcile encola el ReconcileJob de reconexión (§3.5): cierra los
// PRs que murieron durante la desconexión vía ListOpenPRs. La api solo
// encola; un fallo de cola se registra y el PUT ya cambió el flag — el admin
// reintenta habilitando de nuevo (la cola caída es un problema mayor visible
// en el panel de jobs).
func (s *Server) enqueueReconcile(r *http.Request, repoID int64) {
	args, err := json.Marshal(jobs.ReconcileJobArgs{RepositoryID: repoID})
	if err == nil {
		err = s.queue.Enqueue(r.Context(), jobs.KindReconcile, args)
	}
	if err != nil {
		slog.Error("encolando ReconcileJob tras reconexión",
			"repo_id", repoID, "err", err, "req_id", r.Context().Value(requestIDKey))
	}
}

// enqueueIndex encola el IndexJob del repo (§6 F4) tras conectar o
// reconectar — la guarda de proveedor embedding vive en jobs.EnqueueIndexJob
// (§9.6: sin proveedor queda latente, no es error). Best-effort igual que
// enqueueReconcile: un fallo de cola se registra y no bloquea la conexión.
func (s *Server) enqueueIndex(r *http.Request, repoID int64) {
	if err := jobs.EnqueueIndexJob(r.Context(), s.store, s.queue, repoID); err != nil {
		slog.Error("encolando IndexJob", "repo_id", repoID, "err", err,
			"req_id", r.Context().Value(requestIDKey))
	}
}

// handleDeleteRepo: la baja es un flag, no un delete (§3.5) — 405 siempre.
func (s *Server) handleDeleteRepo(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "la desconexión es el flag enabled (PUT /api/repos/{id}), no hay delete")
}

// requireReviewProvider aplica la guarda de rol §9.6: al menos un proveedor
// LLM enabled en el rol review.
func (s *Server) requireReviewProvider(r *http.Request) error {
	providers, err := s.store.ListEnabledLlmProvidersByRole(r.Context(), "review")
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		return errNoReviewProvider
	}
	return nil
}

// writeGuardRejection mapea el resultado de la guarda: 422 con mensaje claro
// (§9.6) o 500 si la consulta misma falló.
func writeGuardRejection(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoReviewProvider) {
		writeError(w, http.StatusUnprocessableEntity,
			"conectar un repo exige al menos un proveedor LLM enabled en el rol review (sin él, cada PR quemaría reintentos hasta fallar)")
		return
	}
	writeError(w, http.StatusInternalServerError, "error interno")
}
