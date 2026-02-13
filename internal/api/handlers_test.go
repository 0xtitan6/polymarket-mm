package api

import (
	"testing"

	"polymarket-mm/internal/config"
)

func TestIsOriginAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		origin  string
		cfg     config.DashboardConfig
		reqHost string
		want    bool
	}{
		{
			name:    "empty origin is allowed",
			origin:  "",
			cfg:     config.DashboardConfig{},
			reqHost: "localhost:8080",
			want:    true,
		},
		{
			name:    "localhost origin allowed by default",
			origin:  "http://localhost:8080",
			cfg:     config.DashboardConfig{},
			reqHost: "localhost:8080",
			want:    true,
		},
		{
			name:    "non-local origin denied by default",
			origin:  "https://evil.example",
			cfg:     config.DashboardConfig{},
			reqHost: "localhost:8080",
			want:    false,
		},
		{
			name:    "allowlist permits exact origin",
			origin:  "https://dash.example.com",
			cfg:     config.DashboardConfig{AllowedOrigins: []string{"https://dash.example.com"}},
			reqHost: "0.0.0.0:8080",
			want:    true,
		},
		{
			name:    "allowlist denies everything else",
			origin:  "https://evil.example",
			cfg:     config.DashboardConfig{AllowedOrigins: []string{"https://dash.example.com"}},
			reqHost: "0.0.0.0:8080",
			want:    false,
		},
		{
			name:    "same host allowed when no allowlist",
			origin:  "https://mm.internal:8080",
			cfg:     config.DashboardConfig{},
			reqHost: "mm.internal:8080",
			want:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isOriginAllowed(tt.origin, tt.cfg, tt.reqHost); got != tt.want {
				t.Fatalf("isOriginAllowed(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}
