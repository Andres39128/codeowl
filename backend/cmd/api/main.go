// Servidor HTTP de codeowl: webhooks VCS + REST del dashboard (mapa: servicios.api).
// F0: configuración esencial, migraciones, seed del admin y la superficie de
// auth (login/logout/session + healthz); webhooks y REST de settings en F1+.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/api"
	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
	vcsgh "github.com/Andres39128/codeowl/backend/internal/vcs/github"
	"github.com/Andres39128/codeowl/backend/migrations"
)

// shutdownGrace es la ventana para drenar conexiones al recibir SIGTERM.
const shutdownGrace = 10 * time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil))) // logs JSON (§9.9)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuración inválida", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("no se pudo conectar a la base de datos", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Ping(ctx); err != nil {
		slog.Error("la base de datos no responde", "err", err)
		os.Exit(1)
	}

	// Migraciones forward-only (§3.3) antes de servir: el schema de River entra
	// acá también — la tabla de jobs existe desde F0 (§6 F0).
	if err := store.Migrate(cfg.DatabaseURL, migrations.FS); err != nil {
		slog.Error("migraciones", "err", err)
		os.Exit(1)
	}

	if err := store.SeedAdmin(ctx, st, cfg); err != nil {
		slog.Error("seed del admin", "err", err)
		os.Exit(1)
	}

	// Cola de jobs: el webhook encola ReviewJob; el worker (otro proceso)
	// los consume. Sin Start acá: la api es insert-only (§3.5).
	jq, err := jobs.New(ctx, st.Pool)
	if err != nil {
		slog.Error("cola de jobs", "err", err)
		os.Exit(1)
	}

	// Routing y middleware (auth, logging, recover) viven en internal/api.
	// El webhook GitHub entra como handler del adapter: sin CSRF (§3.4) —
	// su autenticación es la firma HMAC del payload (§9.3).
	gh := vcsgh.New(st, cfg, jq)
	srv := &http.Server{
		Addr: cfg.APIAddr,
		Handler: api.New(st, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gh.HandleWebhook(r.Context(), w, r)
		})).Routes(),
		ReadHeaderTimeout: shutdownGrace,
	}

	go func() {
		slog.Info("api listening " + cfg.APIAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("sirviendo http", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("apagando la api")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("apagado ordenado", "err", err)
	}
}
