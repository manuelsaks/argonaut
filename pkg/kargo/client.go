package kargo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/darksworm/argonaut/pkg/api"
)

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("Kargo HTTP %d: %s", e.Status, e.Message) }
func NeedsLogin(err error) bool   { var e *APIError; return errors.As(err, &e) && e.Status == 401 }
func IsForbidden(err error) bool  { var e *APIError; return errors.As(err, &e) && e.Status == 403 }

type Client struct {
	Connection   Connection
	mu           sync.Mutex
	authMu       sync.Mutex
	version      string
	projectProbe []byte
}

var resourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
var errNonJSON = errors.New("Kargo returned non-JSON data; verify the API server URL")

func NewClient(connection Connection) (*Client, error) {
	// Pin the API address before any asynchronous operation or confirmation.
	if connection.Server == "" {
		cfg, err := connection.Credentials()
		if err != nil && cfg.APIAddress == "" {
			return nil, err
		}
		connection.Server = cfg.APIAddress
	}
	server, err := NormalizeServer(connection.Server)
	if err != nil {
		return nil, err
	}
	connection.Server = server
	return &Client{Connection: connection}, nil
}

func (c *Client) credentials(ctx context.Context) (Credentials, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	cfg, err := c.Connection.Credentials()
	if err != nil {
		return cfg, err
	}
	if tokenNeedsRefresh(cfg.BearerToken, time.Now()) {
		if cfg.Insecure || cfg.RefreshToken == "" {
			return cfg, &APIError{Status: 401, Message: "session expired; use Ctrl+L for SSO"}
		}
		if err := c.Connection.RefreshSession(ctx); err != nil {
			return cfg, err
		}
		return c.Connection.Credentials()
	}
	return cfg, nil
}

// request never retries a write, follows another origin, or installs a timeout
// shorter than the call site's context. A lost response may mean a write ran.
func (c *Client) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	data, err := c.requestOnce(ctx, method, path, body)
	if method != http.MethodGet || !NeedsLogin(err) {
		return data, err
	}
	c.authMu.Lock()
	cfg, readErr := c.Connection.Credentials()
	if readErr != nil || cfg.Insecure || cfg.RefreshToken == "" {
		c.authMu.Unlock()
		return data, err
	}
	refreshErr := c.Connection.RefreshSession(ctx)
	c.authMu.Unlock()
	if refreshErr != nil {
		return nil, refreshErr
	}
	return c.requestOnce(ctx, method, path, body)
}

func (c *Client) requestOnce(ctx context.Context, method, path string, body any) ([]byte, error) {
	cfg, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.APIAddress+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
	req.Header.Set("Accept", "application/json")
	if strings.Contains(path, connectService) {
		req.Header.Set("Connect-Protocol-Version", "1")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := api.NewHTTPClient(cfg.Insecure)
	if method != http.MethodGet {
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return fmt.Errorf("refusing to redirect a Kargo write") }
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Kargo request failed: %w", err)
	}
	defer response.Body.Close()
	const maxBody = 32 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read Kargo response: %w", err)
	}
	if len(data) > maxBody {
		return nil, fmt.Errorf("Kargo response exceeds 32 MiB; narrow the project/stage/warehouse filter")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		message := payload.Message
		if message == "" {
			message = payload.Error
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		// A misbehaving proxy must never echo a token into the TUI/logs.
		message = strings.ReplaceAll(message, cfg.BearerToken, "[redacted]")
		if cfg.RefreshToken != "" {
			message = strings.ReplaceAll(message, cfg.RefreshToken, "[redacted]")
		}
		return nil, &APIError{Status: response.StatusCode, Message: message}
	}
	if !json.Valid(data) {
		return nil, errNonJSON
	}
	return data, nil
}

func (c *Client) apiVersion(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != "" {
		return c.version, nil
	}
	for _, version := range []string{"v1beta1", "v1alpha1"} {
		data, err := c.request(ctx, http.MethodGet, "/"+version+"/projects", nil)
		if err == nil {
			c.version = version
			c.projectProbe = data
			return version, nil
		}
		var e *APIError
		if !errors.Is(err, errNonJSON) && (!errors.As(err, &e) || e.Status != 404) {
			return "", err
		}
	}
	data, err := c.rpc(ctx, "ListProjects", map[string]int{"pageSize": 100, "page": 0})
	if err != nil {
		return "", fmt.Errorf("Kargo REST/Connect API unavailable: %w", err)
	}
	c.version, c.projectProbe = "connect", data
	return c.version, nil
}

func validateNames(names ...string) error {
	for _, name := range names {
		if !resourceName.MatchString(name) || name == "." || name == ".." || len(name) > 253 {
			return fmt.Errorf("invalid Kargo resource name")
		}
	}
	return nil
}

func (c *Client) List(ctx context.Context, kind, project, stage, warehouse string) ([]Resource, error) {
	version, err := c.apiVersion(ctx)
	if err != nil {
		return nil, err
	}
	if kind == "Project" {
		c.mu.Lock()
		probe := c.projectProbe
		c.projectProbe = nil
		c.mu.Unlock()
		if probe != nil {
			if version == "connect" {
				return c.collectConnectProjects(ctx, probe)
			}
			return decodeList(probe, kind)
		}
	}
	if version == "connect" {
		return c.listConnect(ctx, kind, project, stage, warehouse)
	}
	path := "/" + version + "/projects"
	if kind != "Project" {
		if err := validateNames(project); err != nil {
			return nil, err
		}
		plural := map[string]string{"Stage": "stages", "Promotion": "promotions", "Warehouse": "warehouses", "Freight": "freight"}[kind]
		if plural == "" {
			return nil, fmt.Errorf("unsupported Kargo kind")
		}
		path += "/" + url.PathEscape(project) + "/" + plural
	}
	query := url.Values{}
	if stage != "" {
		if err := validateNames(stage); err != nil {
			return nil, err
		}
		query.Set("stage", stage)
	}
	if warehouse != "" {
		if err := validateNames(warehouse); err != nil {
			return nil, err
		}
		query.Set("origins", warehouse)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	data, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resources, err := decodeList(data, kind)
	if err != nil {
		return nil, err
	}
	for i := range resources {
		resources[i], err = bindResource(resources[i], project, "")
		if err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func decodeList(data []byte, kind string) ([]Resource, error) {
	var envelope struct {
		Items      []json.RawMessage `json:"items"`
		Projects   []json.RawMessage `json:"projects"`
		Stages     []json.RawMessage `json:"stages"`
		Promotions []json.RawMessage `json:"promotions"`
		Warehouses []json.RawMessage `json:"warehouses"`
		Groups     map[string]struct {
			Items   []json.RawMessage `json:"items"`
			Freight []json.RawMessage `json:"freight"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode Kargo list: %w", err)
	}
	for _, group := range envelope.Groups {
		envelope.Items = append(envelope.Items, group.Items...)
		envelope.Items = append(envelope.Items, group.Freight...)
	}
	for _, items := range [][]json.RawMessage{envelope.Projects, envelope.Stages, envelope.Promotions, envelope.Warehouses} {
		envelope.Items = append(envelope.Items, items...)
	}
	resources := make([]Resource, 0, len(envelope.Items))
	seen := map[string]bool{}
	for _, raw := range envelope.Items {
		r, err := DecodeResource(raw, kind)
		if err != nil {
			return nil, err
		}
		key := r.Project + "/" + r.Name
		if !seen[key] {
			resources = append(resources, r)
			seen[key] = true
		}
	}
	SortResources(resources)
	return resources, nil
}

func (c *Client) Get(ctx context.Context, kind, project, name string) (Resource, error) {
	if err := validateNames(name); err != nil {
		return Resource{}, err
	}
	version, err := c.apiVersion(ctx)
	if err != nil {
		return Resource{}, err
	}
	if version == "connect" {
		return c.getConnect(ctx, kind, project, name)
	}
	path := "/" + version + "/projects/"
	if kind == "Project" {
		path += url.PathEscape(name)
	} else {
		if err := validateNames(project); err != nil {
			return Resource{}, err
		}
		plural := map[string]string{"Stage": "stages", "Promotion": "promotions", "Warehouse": "warehouses", "Freight": "freight"}[kind]
		if plural == "" {
			return Resource{}, fmt.Errorf("unsupported Kargo kind")
		}
		path += url.PathEscape(project) + "/" + plural + "/" + url.PathEscape(name)
	}
	data, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Resource{}, err
	}
	resource, err := DecodeResource(data, kind)
	if err != nil {
		return resource, err
	}
	if kind == "Project" {
		project = ""
	}
	return bindResource(resource, project, name)
}

func (c *Client) Promote(ctx context.Context, project, stage, freight string) (Resource, error) {
	if err := validateNames(project, stage, freight); err != nil {
		return Resource{}, err
	}
	version, err := c.apiVersion(ctx)
	if err != nil {
		return Resource{}, err
	}
	if version == "connect" {
		return c.promoteConnect(ctx, project, stage, freight)
	}
	data, err := c.request(ctx, http.MethodPost, "/"+version+"/projects/"+url.PathEscape(project)+"/stages/"+url.PathEscape(stage)+"/promotions", map[string]string{"freight": freight})
	if err != nil {
		return Resource{}, fmt.Errorf("promotion response unavailable; check Promotions before trying again: %w", err)
	}
	resource, err := DecodeResource(data, "Promotion")
	if err != nil {
		return resource, err
	}
	return bindResource(resource, project, "")
}
