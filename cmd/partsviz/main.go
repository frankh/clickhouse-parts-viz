package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	partsviz "github.com/frankh/clickhouse-parts-viz"
	"github.com/frankh/clickhouse-parts-viz/internal/api"
	"github.com/frankh/clickhouse-parts-viz/internal/ch"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	client, err := ch.New(env("CLICKHOUSE_URL", "http://localhost:8123"), os.Getenv("CLICKHOUSE_USER"), os.Getenv("CLICKHOUSE_PASSWORD"))
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if v := os.Getenv("CLICKHOUSE_MAX_EXECUTION_TIME"); v != "" {
		if client.MaxExecutionTime, err = strconv.Atoi(v); err != nil {
			slog.Error("config: CLICKHOUSE_MAX_EXECUTION_TIME", "err", err)
			os.Exit(1)
		}
	}
	store := &ch.Store{Client: client, Cluster: os.Getenv("CLICKHOUSE_CLUSTER")}

	mux := http.NewServeMux()
	api.New(store).Routes(mux)
	web, _ := fs.Sub(partsviz.Web, "web")
	mux.Handle("GET /", http.FileServerFS(web))

	addr := env("LISTEN_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("listening", "addr", addr, "cluster", store.Cluster)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
