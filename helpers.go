package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func getString(req mcp.CallToolRequest, key string) string {
	v, _ := req.Params.Arguments[key].(string)
	return v
}

func getBool(req mcp.CallToolRequest, key string) bool {
	v, _ := req.Params.Arguments[key].(bool)
	return v
}

// parseTags splits a comma-separated tag string, trims whitespace,
// and filters out empty strings.
func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// localIP returns the preferred outbound LAN IP (e.g. 192.168.x.x).
// Falls back to "localhost" if no suitable non-loopback IPv4 address is found.
func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "localhost"
}

// formatManageURL returns a human-readable startup message showing both
// localhost and LAN addresses the management UI is reachable at.
func formatManageURL(port string) string {
	ip := localIP()
	if ip == "localhost" {
		return fmt.Sprintf("http://localhost:%s", port)
	}
	return fmt.Sprintf("http://localhost:%s  (LAN: http://%s:%s)", port, ip, port)
}

// findConfigPath returns the path to the config file.
// It first checks the executable directory; if no config exists there (e.g. go run),
// it falls back to knowledge-mcp.toml in the current working directory.
func findConfigPath() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "knowledge-mcp.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// go run / dev mode: use CWD
	return filepath.Join(".", "knowledge-mcp.toml")
}

// parseTime parses an ISO 8601 date string, supporting both date-only
// ("2006-01-02") and full RFC 3339 ("2006-01-02T15:04:05Z07:00") formats.
// Returns the zero time on empty input or parse failure.
func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t
	}
	return time.Time{}
}

