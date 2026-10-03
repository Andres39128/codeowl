// Paquete github implementa el contrato vcs.VCSProvider para GitHub
// (mapa: backend.vcs github): webhooks firmados, API REST con token de
// instalación y clonado shallow por git. Sin SDK: la superficie usada es
// pequeña y stdlib-first (§3.3).
package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// La instalación de la App se resuelve por repo (la tabla repositories solo
// guarda el external_id del repo) y se cachea en memoria con TTL corto: una
// desinstalación/reinstalación de la App se recupera sola.
const (
	jwtTTL          = 10 * time.Minute // tope que acepta GitHub
	jwtClockSkew    = 60 * time.Second // deriva de reloj tolerada
	tokenRefreshAge = 5 * time.Minute  // refresco del token antes de expirar
	installationTTL = time.Hour
)

// tokenManager genera y cachea en memoria los tokens de instalación de la
// GitHub App. El token es efímero (~1h) y nunca se persiste en BD (§6 F1).
type tokenManager struct {
	appID   string
	key     *rsa.PrivateKey
	http    *http.Client
	baseURL string

	mu     sync.Mutex
	tokens map[int64]cachedToken         // por installation ID
	inst   map[string]cachedInstallation // "owner/name" → installation ID
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

type cachedInstallation struct {
	id        int64
	fetchedAt time.Time
}

// newTokenManager parsea la private key PEM de la App (PKCS8 o PKCS1).
func newTokenManager(appID, privateKeyPEM string, hc *http.Client, baseURL string) (*tokenManager, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &tokenManager{
		appID:   appID,
		key:     key,
		http:    hc,
		baseURL: baseURL,
		tokens:  map[int64]cachedToken{},
		inst:    map[string]cachedInstallation{},
	}, nil
}

// parsePrivateKey acepta el PEM PKCS8 (formato que exporta GitHub al crear
// la App) y el PKCS1 clásico ("RSA PRIVATE KEY").
func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("private key de la App sin bloque PEM (¿GITHUB_APP_PRIVATE_KEY incompleta?)")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("private key de la App no es PKCS8 ni PKCS1: %w", err)
		}
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("la private key de la App debe ser RSA, got %T", parsed)
	}
	return key, nil
}

// jwt firma un JWT RS256 (header.claims.firma, base64url sin padding) con
// la private key de la App: credencial para operar la App en la API.
func (tm *tokenManager) jwt() (string, error) {
	now := time.Now()
	type claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims{
		Iss: tm.appID,
		Iat: now.Add(-jwtClockSkew).Unix(),
		Exp: now.Add(jwtTTL).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("serializando claims del JWT: %w", err)
	}
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, tm.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("firmando JWT de la App: %w", err)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Token devuelve el token de instalación cacheado o lo intercambia por un
// JWT fresco (POST /app/installations/{id}/access_tokens). Se refresca
// tokenRefreshAge antes de expirar.
func (tm *tokenManager) Token(ctx context.Context, installationID int64) (string, error) {
	tm.mu.Lock()
	if c, ok := tm.tokens[installationID]; ok && time.Until(c.expiresAt) > tokenRefreshAge {
		tm.mu.Unlock()
		return c.value, nil
	}
	tm.mu.Unlock()

	jwt, err := tm.jwt()
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	req, err := newAPIRequest(ctx, http.MethodPost, tm.baseURL,
		fmt.Sprintf("/app/installations/%d/access_tokens", installationID), jwt, mediaTypeJSON, nil)
	if err != nil {
		return "", err
	}
	status, err := tm.do(ctx, req, &out)
	if err != nil {
		return "", fmt.Errorf("intercambiando JWT por token de instalación %d: %w", installationID, err)
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("intercambiando JWT por token de instalación %d: status %d", installationID, status)
	}
	tm.mu.Lock()
	tm.tokens[installationID] = cachedToken{value: out.Token, expiresAt: out.ExpiresAt}
	tm.mu.Unlock()
	return out.Token, nil
}

// InstallationID resuelve (y cachea) el installation ID de la App sobre el
// repo — la fila del repo solo conoce el external_id del repo.
func (tm *tokenManager) InstallationID(ctx context.Context, repo *store.Repository) (int64, error) {
	name := repo.Owner + "/" + repo.Name
	tm.mu.Lock()
	if c, ok := tm.inst[name]; ok && time.Since(c.fetchedAt) < installationTTL {
		tm.mu.Unlock()
		return c.id, nil
	}
	tm.mu.Unlock()

	jwt, err := tm.jwt()
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	req, err := newAPIRequest(ctx, http.MethodGet, tm.baseURL,
		fmt.Sprintf("/repos/%s/%s/installation", repo.Owner, repo.Name), jwt, mediaTypeJSON, nil)
	if err != nil {
		return 0, err
	}
	status, err := tm.do(ctx, req, &out)
	if err != nil {
		return 0, fmt.Errorf("resolviendo instalación de la App para %s: %w", name, err)
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("resolviendo instalación de la App para %s: status %d (¿App instalada en el repo?)", name, status)
	}
	tm.mu.Lock()
	tm.inst[name] = cachedInstallation{id: out.ID, fetchedAt: time.Now()}
	tm.mu.Unlock()
	return out.ID, nil
}

// do ejecuta la request y decodea la respuesta JSON en out si no es nil.
// Devuelve el status HTTP para que el caller clasifique (404 vs 5xx...).
func (tm *tokenManager) do(ctx context.Context, req *http.Request, out any) (int, error) {
	resp, err := tm.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decodificando respuesta %s %s: %w", req.Method, req.URL.Path, err)
		}
	} else if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	}
	return resp.StatusCode, nil
}
