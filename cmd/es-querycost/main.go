package main

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"es-querycost/internal/config"
	"es-querycost/internal/logger"
	"es-querycost/internal/proxy"
	"github.com/spf13/pflag"
)

func main() {
	cfg, configPath, err := config.Load()
	if err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return
		}
		stdlog.Fatalf("failed to load config: %v", err)
	}

	log := logger.New(logger.Config{
		Level:    cfg.LogLevel,
		Format:   cfg.LogFormat,
		Requests: cfg.LogRequests,
	}, nil)
	log.Info("starting es-querycost", "listen_addr", cfg.ListenAddr, "es_url", cfg.ElasticsearchURL)

	server, err := proxy.NewServer(cfg, log)
	if err != nil {
		log.Error("failed to create server", "error", err)
		stdlog.Fatalf("failed to create server: %v", err)
	}

	if configPath != "" {
		cancel := config.Watch(configPath, func(newCfg config.Config) {
			log.Info("config reloaded", "path", configPath)
			if err := server.UpdateConfig(newCfg); err != nil {
				log.Error("failed to apply reloaded config", "error", err)
			} else {
				log.Info("config applied")
			}
		})
		defer cancel()
	}

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: server.Handler(),
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("es-querycost listening on %s, proxying to %s\n", cfg.ListenAddr, cfg.ElasticsearchURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Error("server exited", "error", err)
		stdlog.Fatalf("server exited: %v", err)
	case sig := <-sigCh:
		log.Info("received signal, shutting down", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Error("shutdown error", "error", err)
		}
		server.Close()
	}
}
