package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"polymarket-mm/internal/config"
)

// Server runs the HTTP/WebSocket API for the dashboard
type Server struct {
	cfg      config.DashboardConfig
	provider MarketSnapshotProvider
	fullCfg  config.Config
	hub      *Hub
	handlers *Handlers
	server   *http.Server
	logger   *slog.Logger
}

// NewServer creates a new API server
func NewServer(
	cfg config.DashboardConfig,
	provider MarketSnapshotProvider,
	fullCfg config.Config,
	logger *slog.Logger,
) *Server {
	hub := NewHub(logger)
	handlers := NewHandlers(provider, fullCfg, hub, logger)

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/health", handlers.HandleHealth)
	mux.HandleFunc("/api/snapshot", handlers.HandleSnapshot)
	mux.HandleFunc("/ws", handlers.HandleWebSocket)

	// Serve static files (web dashboard)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return &Server{
		cfg:      cfg,
		provider: provider,
		fullCfg:  fullCfg,
		hub:      hub,
		handlers: handlers,
		server:   server,
		logger:   logger.With("component", "api-server"),
	}
}

// Start starts the API server and hub
func (s *Server) Start() error {
	// Start WebSocket hub
	go s.hub.Run()

	// Start event consumer
	go s.consumeEvents()

	s.logger.Info("dashboard server starting", "addr", s.server.Addr)

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

// Stop gracefully stops the server
func (s *Server) Stop() error {
	s.logger.Info("stopping dashboard server")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// consumeEvents reads events from the engine and broadcasts them
func (s *Server) consumeEvents() {
	eventsCh := s.provider.(interface {
		DashboardEvents() <-chan DashboardEvent
	}).DashboardEvents()

	if eventsCh == nil {
		return
	}

	for evt := range eventsCh {
		s.hub.BroadcastEvent(evt)
	}
}
