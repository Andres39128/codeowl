package github

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// El token de instalación autentica el clonado HTTPS de GitHub. Para que no
// aparezca en argv (visible en /proc) ni en el remote URL (queda en .git/
// config), viaja por una variable de entorno que lee un helper GIT_ASKPASS
// efímero: el helper vive en el tmp del proceso y se borra al terminar.
const (
	askpassEnv    = "CODEOWL_GIT_TOKEN"
	askpassBody   = "#!/bin/sh\necho \"$" + askpassEnv + "\"\n"
	gitTerminal   = "GIT_TERMINAL_PROMPT=0" // sin prompts interactivos: falla rápida
	gitAskpassEnv = "GIT_ASKPASS"
)

// gitAuth agrupa la credencial efímera del clonado: el path del helper
// askpass y el token que este entrega.
type gitAuth struct {
	askpass string
	token   string
}

// newGitAuth escribe el helper askpass (permisos 0700, único por llamada).
func newGitAuth(token string) (*gitAuth, error) {
	f, err := os.CreateTemp("", "codeowl-askpass-")
	if err != nil {
		return nil, fmt.Errorf("creando helper askpass: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(askpassBody); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("escribiendo helper askpass: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("cerrando helper askpass: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("dando permisos al helper askpass: %w", err)
	}
	return &gitAuth{askpass: path, token: token}, nil
}

// cleanup borra el helper askpass.
func (g *gitAuth) cleanup() {
	if g != nil && g.askpass != "" {
		_ = os.Remove(g.askpass)
	}
}

// git ejecuta un comando git en workdir con la credencial efímera inyectada
// por entorno. El ctx gobierna el timeout del job.
func (a *Adapter) git(ctx context.Context, workdir string, auth *gitAuth, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		gitTerminal,
		gitAskpassEnv+"="+auth.askpass,
		askpassEnv+"="+auth.token,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
