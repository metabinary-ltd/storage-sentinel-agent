package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/metabinary-ltd/storagesentinel/internal/config"
	"github.com/metabinary-ltd/storagesentinel/internal/health"
	"github.com/metabinary-ltd/storagesentinel/internal/notifier"
	"github.com/metabinary-ltd/storagesentinel/internal/storage"
)

type Server struct {
	cfg       config.APIConfig
	logger    *slog.Logger
	srv       *http.Server
	health    health.Provider
	store     *storage.Store
	notifier  *notifier.Notifier
	mux       *http.ServeMux
	started   bool
	authToken string
	triggers  Triggers
}

type Triggers struct {
	CollectSmart func(context.Context) error
	CollectNvme  func(context.Context) error
	CollectZfs   func(context.Context) error
	TriggerScrub func(context.Context, string) error
}

func NewServer(cfg config.APIConfig, store *storage.Store, healthProvider health.Provider, notifier *notifier.Notifier, triggers Triggers, logger *slog.Logger) *Server {
	mux := http.NewServeMux()
	s := &Server{
		cfg:       cfg,
		logger:    logger,
		health:    healthProvider,
		store:     store,
		notifier:  notifier,
		mux:       mux,
		authToken: strings.TrimSpace(cfg.AuthToken),
		triggers:  triggers,
	}
	s.registerRoutes()
	s.srv = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port),
		Handler: s.mux,
		BaseContext: func(l net.Listener) context.Context {
			return context.Background()
		},
	}
	return s
}

func (s *Server) Start() error {
	s.logger.Info("starting api server", "addr", s.srv.Addr)
	s.started = true
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if !s.started {
		return nil
	}
	s.logger.Info("stopping api server")
	return s.srv.Shutdown(ctx)
}
