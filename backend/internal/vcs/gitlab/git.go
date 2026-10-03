package gitlab

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// El clonado GitLab autentica con la deploy key SSH del repo (guía §3.3): la
// clave privada viaja a un archivo efímero (0600, tmp del proceso, borrado al
// salir) y git la recibe por GIT_SSH_COMMAND — jamás en argv (visible en
// /proc) ni en el remote URL (queda en .git/config).
type sshAuth struct {
	keyFile string
}

// sshOptions son las opciones del GIT_SSH_COMMAND: sin agentes ni otras
// identidades, sin prompts interactivos (falla rápida) y con TOFU para el
// host (accept-new registra el known_hosts en el primer clonado).
const sshOptions = "-o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new"

// newSSHAuth escribe la clave privada al archivo efímero.
func newSSHAuth(privateKey string) (*sshAuth, error) {
	f, err := os.CreateTemp("", "codeowl-deploykey-")
	if err != nil {
		return nil, fmt.Errorf("creando archivo de deploy key: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(strings.TrimSpace(privateKey) + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("escribiendo deploy key: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("cerrando archivo de deploy key: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("dando permisos a la deploy key: %w", err)
	}
	return &sshAuth{keyFile: path}, nil
}

// cleanup borra el archivo de la clave.
func (s *sshAuth) cleanup() {
	if s != nil && s.keyFile != "" {
		_ = os.Remove(s.keyFile)
	}
}

// gitSSHCommand arma la variable de entorno con la clave inyectada.
func (s *sshAuth) gitSSHCommand() string {
	return "GIT_SSH_COMMAND=ssh -i " + s.keyFile + " " + sshOptions
}

// sshRemote arma el remote SSH del repo: git@{host}:{owner}/{name}.git — el
// host sale de la base de la instancia sin el esquema (gitlab.com o
// self-managed, §3.3).
func (a *Adapter) sshRemote(repo *store.Repository) string {
	base := a.apiBase(repo)
	host := base
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		host = u.Host
	}
	return fmt.Sprintf("git@%s:%s/%s.git", host, repo.Owner, repo.Name)
}

// git ejecuta un comando git en workdir con la deploy key efímera inyectada
// por entorno. El ctx gobierna el timeout del job.
func (a *Adapter) git(ctx context.Context, workdir string, auth *sshAuth, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // sin prompts interactivos: falla rápida
		auth.gitSSHCommand(),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
