package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Listen            string
	PBXAPIURL         string
	PBXAPICAFile      string
	PBXAPITLSInsecure bool
	PrivateKey        string
	MasterKey         string
	WebSecret         string
	StateDir          string
	HistoryTTL        time.Duration
	HTTPTimeout       time.Duration
	MaxBodyBytes      int64
}

func loadConfig() config {
	pbxAPIURL := env("ISSABEL_PBXAPI_URL", "https://127.0.0.1/pbxapi")
	return config{
		Listen:            env("ISSABEL_MCP_LISTEN", "127.0.0.1:8787"),
		PBXAPIURL:         pbxAPIURL,
		PBXAPICAFile:      env("ISSABEL_PBXAPI_CA_FILE", ""),
		PBXAPITLSInsecure: envBool("ISSABEL_PBXAPI_TLS_INSECURE", isLoopbackURL(pbxAPIURL)),
		PrivateKey:        env("ISSABEL_MCP_PRIVATE_KEY", "/etc/issabel-mcp/private.pem"),
		MasterKey:         env("ISSABEL_MCP_MASTER_KEY", "/etc/issabel-mcp/master.key"),
		WebSecret:         env("ISSABEL_MCP_WEB_SECRET", "/etc/issabel-mcp/web.secret"),
		StateDir:          env("ISSABEL_MCP_STATE_DIR", "/var/lib/issabel-mcp"),
		HistoryTTL:        time.Duration(envInt("ISSABEL_MCP_HISTORY_DAYS", 30)) * 24 * time.Hour,
		HTTPTimeout:       time.Duration(envInt("ISSABEL_MCP_HTTP_TIMEOUT_SECONDS", 45)) * time.Second,
		MaxBodyBytes:      int64(envInt("ISSABEL_MCP_MAX_BODY_BYTES", 1<<20)),
	}
}

func isLoopbackURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c config) validate(mode string) error {
	if c.PBXAPIURL == "" || c.StateDir == "" {
		return fmt.Errorf("PBX API URL and state directory are required")
	}
	if mode == "serve" {
		if _, err := os.Stat(c.MasterKey); err != nil {
			return fmt.Errorf("master key: %w", err)
		}
		if _, err := os.Stat(c.WebSecret); err != nil {
			return fmt.Errorf("web secret: %w", err)
		}
	}
	if _, err := os.Stat(c.PrivateKey); err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	return os.MkdirAll(filepath.Clean(c.StateDir), 0700)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envBool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
