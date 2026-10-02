// Worker de codeowl: consume jobs River de Postgres (mapa: servicios.worker).
// F0 esqueleto: config, store y apagado ordenado — el wiring de River
// (internal/jobs) aterriza en T4.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

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

	// Sin migraciones ni seed: la unidad api las ejecuta al arranque. El
	// wiring de River (internal/jobs) aterriza en T4.
	slog.Info("worker esqueleto corriendo")
	<-ctx.Done()
	slog.Info("apagando el worker")
}
