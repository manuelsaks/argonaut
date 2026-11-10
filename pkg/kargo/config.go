package kargo

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Connection binds requests and CLI authentication to one Kargo configuration.
// Home is an XDG_CONFIG_HOME directory, not a kubeconfig or token file.
type Connection struct {
	Name, Server, Home, Command string
	CallbackPort                int
	AutoRelogin                 bool
}

type Credentials struct {
	APIAddress   string `yaml:"apiAddress"`
	BearerToken  string `yaml:"bearerToken"`
	RefreshToken string `yaml:"refreshToken"`
	AuthMethod   string `yaml:"authMethod"`
	Insecure     bool   `yaml:"insecureSkipTLSVerify"`
	CallbackPort int    `yaml:"callbackPort"`
}

func DefaultHome() string {
	if home := os.Getenv("XDG_CONFIG_HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func (c Connection) ConfigPath() string {
	home := c.Home
	if home == "" {
		home = DefaultHome()
	}
	return filepath.Join(home, "kargo", "config")
}

func NormalizeServer(server string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Kargo server must be an HTTP(S) URL without credentials, query or fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (c Connection) Credentials() (Credentials, error) {
	var cfg Credentials
	data, err := os.ReadFile(c.ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, &APIError{Status: 401, Message: "Kargo CLI session unavailable; use Ctrl+L for SSO login"}
		}
		return cfg, fmt.Errorf("cannot read Kargo CLI configuration")
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("cannot parse Kargo CLI configuration")
	}
	server, err := NormalizeServer(cfg.APIAddress)
	if err != nil {
		return cfg, err
	}
	if c.Server != "" {
		expected, err := NormalizeServer(c.Server)
		if err != nil {
			return cfg, err
		}
		if server != expected {
			return cfg, fmt.Errorf("Kargo CLI server changed; select the connection again before continuing")
		}
	}
	cfg.APIAddress = server
	if cfg.BearerToken == "" {
		return cfg, &APIError{Status: 401, Message: "Kargo CLI has no session; use Ctrl+L for SSO"}
	}
	return cfg, nil
}

func (c Connection) command(ctx context.Context, args ...string) *exec.Cmd {
	command := c.Command
	if command == "" {
		command = "kargo"
	}
	cmd := exec.CommandContext(ctx, command, args...)
	// Kargo environment overrides could otherwise send another connection's
	// credentials or silently override the server / TLS settings selected here.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "XDG_CONFIG_HOME", "KARGO_API_ADDRESS", "KARGO_API_TOKEN", "KARGO_INSECURE_SKIP_TLS_VERIFY", "KUBECONFIG":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	home := c.Home
	if home == "" {
		home = DefaultHome()
	}
	cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+home)
	return cmd
}

func (c Connection) LoginCommand(ctx context.Context) (*exec.Cmd, error) {
	server := c.Server
	port := c.CallbackPort
	if cfg, _ := c.Credentials(); cfg.APIAddress != "" {
		if server == "" {
			server = cfg.APIAddress
		}
		if port == 0 {
			port = cfg.CallbackPort
		}
	}
	server, err := NormalizeServer(server)
	if err != nil {
		return nil, fmt.Errorf("configure a Kargo server or run kargo login <URL> --sso first")
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid Kargo SSO callback port")
	}
	args := []string{"login", server, "--sso"}
	if port != 0 {
		args = append(args, "--port", strconv.Itoa(port))
	}
	return c.command(ctx, args...), nil
}

// The JWT expiry is only a refresh hint. The server still validates identity.
func tokenNeedsRefresh(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	return json.Unmarshal(data, &claims) == nil && claims.Exp > 0 && now.Add(15*time.Second).Unix() >= claims.Exp
}

func (c Connection) SessionIdentity() string {
	cfg, err := c.Credentials()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%t", cfg.APIAddress, cfg.BearerToken, cfg.Insecure)))
	return fmt.Sprintf("%x", sum)
}

// Let the installed CLI redeem and persist rotating refresh tokens. Neither
// Argonaut's config nor process arguments contain credentials.
func (c Connection) RefreshSession(ctx context.Context) error {
	cmd := c.command(ctx, "get", "projects", "--output", "json")
	if err := cmd.Run(); err != nil {
		return &APIError{Status: 401, Message: "Kargo session refresh failed; use Ctrl+L for SSO login"}
	}
	return nil
}
