// Command cf-terminal-relay is the terminal relay of the panel (FR-802). It connects the
// browser of a user to the agent of a server, for the terminal of a website. The panel API
// image has the program, and the unit cf-panel-terminal runs it (quadlet.PanelUnits).
//
// Usage:
//
//	APP_KEY=... cf-terminal-relay [--listen :8090]
//
// APP_KEY is the key of the panel API. The relay makes the key of the tickets from it.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/relay"
)

func main() {
	listen := flag.String("listen", ":8090", "the address of the HTTP server")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	appKey := os.Getenv("APP_KEY")
	if appKey == "" {
		slog.Error("APP_KEY is not set")
		os.Exit(2)
	}
	r := relay.New(relay.Key(appKey))
	srv := &http.Server{Addr: *listen, Handler: r, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		r.Shutdown(context.Background())
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("terminal relay: listening", "address", *listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("terminal relay", "err", err)
		os.Exit(1)
	}
}
