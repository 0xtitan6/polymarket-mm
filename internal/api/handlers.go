package api

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
	"polymarket-mm/internal/config"
)

// Handlers holds all HTTP handler dependencies
type Handlers struct {
	provider MarketSnapshotProvider
	cfg      config.Config
	hub      *Hub
	logger   *slog.Logger
}

// NewHandlers creates a new handlers instance
func NewHandlers(provider MarketSnapshotProvider, cfg config.Config, hub *Hub, logger *slog.Logger) *Handlers {
	return &Handlers{
		provider: provider,
		cfg:      cfg,
		hub:      hub,
		logger:   logger.With("component", "api-handlers"),
	}
}

// HandleHealth returns a simple health check response
func (h *Handlers) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HandleSnapshot returns the current dashboard state
func (h *Handlers) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot := BuildSnapshot(h.provider, h.cfg)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		h.logger.Error("failed to encode snapshot", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
}

// HandleWebSocket upgrades the connection and creates a new WebSocket client
func (h *Handlers) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(req *http.Request) bool {
			return isOriginAllowed(req.Header.Get("Origin"), h.cfg.Dashboard, req.Host)
		},
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("websocket upgrade failed", "error", err)
		return
	}

	// Create new client
	client := NewClient(h.hub, conn)

	// Send initial snapshot to the client
	snapshot := BuildSnapshot(h.provider, h.cfg)
	evt := DashboardEvent{
		Type: "snapshot",
		Data: snapshot,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.Error("failed to marshal initial snapshot", "error", err)
		return
	}

	select {
	case client.send <- data:
	default:
		h.logger.Warn("failed to send initial snapshot to client")
	}
}

func isOriginAllowed(origin string, cfg config.DashboardConfig, reqHost string) bool {
	if origin == "" {
		// Non-browser clients often omit Origin; keep this path functional.
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}

	normalized := normalizeOrigin(originURL.Scheme, originURL.Host)
	if normalized == "" {
		return false
	}

	if len(cfg.AllowedOrigins) > 0 {
		for _, allowed := range cfg.AllowedOrigins {
			u, err := url.Parse(allowed)
			if err != nil {
				continue
			}
			if normalized == normalizeOrigin(u.Scheme, u.Host) {
				return true
			}
		}
		return false
	}

	host := strings.ToLower(originURL.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}

	reqHostname := normalizeHost(reqHost)
	return reqHostname != "" && host == reqHostname
}

func normalizeOrigin(scheme, host string) string {
	if scheme == "" || host == "" {
		return ""
	}
	return strings.ToLower(scheme) + "://" + strings.ToLower(host)
}

func normalizeHost(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(hostport)
}
