// Package client is a small client for the Codigate workspace API (/api/v1, ADR 0014).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client calls one workspace's API with a workspace API token.
type Client struct {
	BaseURL   string
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// New returns a client for baseURL (for example https://app.codigate.nl/api/v1).
func New(baseURL, token, userAgent string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
	}
}

// Error is an API error response: {message, errors: {field: [...]}}.
type Error struct {
	Status  int
	Message string
	Errors  map[string][]string
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "codigate API: %s (HTTP %d)", msg, e.Status)
	fields := make([]string, 0, len(e.Errors))
	for f := range e.Errors {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		fmt.Fprintf(&b, "\n  %s: %s", f, strings.Join(e.Errors[f], " "))
	}
	return b.String()
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// IsValidation reports whether err is a 422 from the API.
func IsValidation(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnprocessableEntity
}

// Do sends body as JSON (when not nil) and decodes the response into out (when not nil).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var body struct {
			Message string              `json:"message"`
			Errors  map[string][]string `json:"errors"`
		}
		_ = json.Unmarshal(raw, &body)
		return &Error{Status: resp.StatusCode, Message: body.Message, Errors: body.Errors}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("codigate API: unexpected response from %s %s: %w", method, path, err)
	}
	return nil
}

type envelope[T any] struct {
	Data T `json:"data"`
}

func get[T any](ctx context.Context, c *Client, path string) (*T, error) {
	return send[T](ctx, c, http.MethodGet, path, nil)
}

func send[T any](ctx context.Context, c *Client, method, path string, body any) (*T, error) {
	var e envelope[T]
	if err := c.Do(ctx, method, path, body, &e); err != nil {
		return nil, err
	}
	return &e.Data, nil
}

func p(format string, args ...string) string {
	escaped := make([]any, len(args))
	for i, a := range args {
		escaped[i] = url.PathEscape(a)
	}
	return fmt.Sprintf(format, escaped...)
}

// Workspace is GET /workspace (not wrapped in data).
type Workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	AppDomain string `json:"app_domain"`
	Plan      string `json:"plan"`
	Suspended bool   `json:"suspended"`
}

func (c *Client) Workspace(ctx context.Context) (*Workspace, error) {
	var w Workspace
	if err := c.Do(ctx, http.MethodGet, "/workspace", nil, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

type Agent struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Connected  bool    `json:"connected"`
	LastSeenAt *string `json:"last_seen_at"`
	Version    *string `json:"version"`
	CreatedAt  *string `json:"created_at"`
}

type agentWithToken struct {
	Data  Agent  `json:"data"`
	Token string `json:"token"`
}

func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	l, err := get[[]Agent](ctx, c, "/agents")
	if err != nil {
		return nil, err
	}
	return *l, nil
}

func (c *Client) Agent(ctx context.Context, id string) (*Agent, error) {
	return get[Agent](ctx, c, p("/agents/%s", id))
}

// CreateAgent returns the agent and its token; the token is only shown here and on rotate.
func (c *Client) CreateAgent(ctx context.Context, name string) (*Agent, string, error) {
	var out agentWithToken
	if err := c.Do(ctx, http.MethodPost, "/agents", map[string]any{"name": name}, &out); err != nil {
		return nil, "", err
	}
	return &out.Data, out.Token, nil
}

func (c *Client) RenameAgent(ctx context.Context, id, name string) (*Agent, error) {
	return send[Agent](ctx, c, http.MethodPatch, p("/agents/%s", id), map[string]any{"name": name})
}

func (c *Client) RotateAgent(ctx context.Context, id string) (*Agent, string, error) {
	var out agentWithToken
	if err := c.Do(ctx, http.MethodPost, p("/agents/%s/rotate", id), nil, &out); err != nil {
		return nil, "", err
	}
	return &out.Data, out.Token, nil
}

func (c *Client) DeleteAgent(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, p("/agents/%s", id), nil, nil)
}

type Policy struct {
	Everyone     bool     `json:"everyone"`
	Emails       []string `json:"emails"`
	EmailDomains []string `json:"email_domains"`
	Groups       []string `json:"groups"`
}

type App struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Hostname       string   `json:"hostname"`
	AgentID        *string  `json:"agent_id"`
	Access         string   `json:"access"`
	IdpID          *string  `json:"idp_id"`
	Policy         *Policy  `json:"policy"`
	ServiceTokens  []string `json:"service_tokens"`
	BypassPaths    []string `json:"bypass_paths"`
	Enabled        bool     `json:"enabled"`
	Live           bool     `json:"live"`
	NotLiveReason  *string  `json:"not_live_reason"`
	AgentConnected bool     `json:"agent_connected"`
	CreatedAt      *string  `json:"created_at"`
}

// AppAccess is the body of PUT /apps/{id}/access.
type AppAccess struct {
	Access        string   `json:"access"`
	IdpID         *string  `json:"idp_id"`
	Policy        *Policy  `json:"policy"`
	ServiceTokens []string `json:"service_tokens"`
	BypassPaths   []string `json:"bypass_paths"`
}

func (c *Client) Apps(ctx context.Context) ([]App, error) {
	l, err := get[[]App](ctx, c, "/apps")
	if err != nil {
		return nil, err
	}
	return *l, nil
}

func (c *Client) App(ctx context.Context, id string) (*App, error) {
	return get[App](ctx, c, p("/apps/%s", id))
}

// NormalizeHostname matches the API: trimmed, lowercase, no trailing dot.
func NormalizeHostname(h string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(h)), ".")
}

// AppByHostname finds an app by hostname; a 404 Error when there is none.
func (c *Client) AppByHostname(ctx context.Context, hostname string) (*App, error) {
	apps, err := c.Apps(ctx)
	if err != nil {
		return nil, err
	}
	want := NormalizeHostname(hostname)
	for i := range apps {
		if apps[i].Hostname == want {
			return &apps[i], nil
		}
	}
	return nil, &Error{Status: http.StatusNotFound, Message: "No app with hostname " + want + "."}
}

func (c *Client) CreateApp(ctx context.Context, name, hostname, agentID string) (*App, error) {
	return send[App](ctx, c, http.MethodPost, "/apps", map[string]any{"name": name, "hostname": hostname, "agent_id": agentID})
}

func (c *Client) UpdateApp(ctx context.Context, id string, changes map[string]any) (*App, error) {
	return send[App](ctx, c, http.MethodPatch, p("/apps/%s", id), changes)
}

func (c *Client) SetAppAccess(ctx context.Context, id string, access AppAccess) (*App, error) {
	return send[App](ctx, c, http.MethodPut, p("/apps/%s/access", id), access)
}

func (c *Client) SetAppEnabled(ctx context.Context, id string, enabled bool) (*App, error) {
	return send[App](ctx, c, http.MethodPut, p("/apps/%s/enabled", id), map[string]any{"enabled": enabled})
}

func (c *Client) DeleteApp(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, p("/apps/%s", id), nil, nil)
}

type PathRule struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type RateLimit struct {
	PerMinute int64 `json:"per_minute"`
}

type Ban struct {
	AfterRateLimits   int64 `json:"after_rate_limits"`
	AfterFailedLogins int64 `json:"after_failed_logins"`
	WindowSeconds     int64 `json:"window_seconds"`
	DurationSeconds   int64 `json:"duration_seconds"`
}

type Challenge struct {
	Mode          string `json:"mode"`
	Difficulty    string `json:"difficulty"`
	CookieMinutes int64  `json:"cookie_minutes"`
	AutoPerMinute *int64 `json:"auto_per_minute"`
}

type Waf struct {
	Mode       string  `json:"mode"`
	Exclusions []int64 `json:"exclusions"`
}

// EdgeRules is both the PUT body and the "rules" part of GET /apps/{id}/edge-rules.
type EdgeRules struct {
	AllowIPs       []string   `json:"allow_ips"`
	DenyIPs        []string   `json:"deny_ips"`
	AllowCountries []string   `json:"allow_countries"`
	DenyCountries  []string   `json:"deny_countries"`
	Paths          []PathRule `json:"paths"`
	RateLimit      *RateLimit `json:"rate_limit"`
	Ban            *Ban       `json:"ban"`
	Challenge      *Challenge `json:"challenge"`
	Waf            *Waf       `json:"waf"`
}

type EdgeRulesView struct {
	AppID           string    `json:"app_id"`
	Rules           EdgeRules `json:"rules"`
	AdvancedAllowed bool      `json:"advanced_allowed"`
	Inactive        []string  `json:"inactive"`
}

func (c *Client) EdgeRules(ctx context.Context, appID string) (*EdgeRulesView, error) {
	return get[EdgeRulesView](ctx, c, p("/apps/%s/edge-rules", appID))
}

// SetEdgeRules replaces all edge rules of the app; a part left out is cleared.
func (c *Client) SetEdgeRules(ctx context.Context, appID string, rules EdgeRules) (*EdgeRulesView, error) {
	return send[EdgeRulesView](ctx, c, http.MethodPut, p("/apps/%s/edge-rules", appID), rules)
}

type IdentityProvider struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	Issuer          string   `json:"issuer"`
	ClientID        string   `json:"client_id"`
	HasClientSecret bool     `json:"has_client_secret"`
	Scopes          []string `json:"scopes"`
	GroupsClaim     string   `json:"groups_claim"`
	RedirectURI     string   `json:"redirect_uri"`
}

func (c *Client) IdentityProviders(ctx context.Context) ([]IdentityProvider, error) {
	l, err := get[[]IdentityProvider](ctx, c, "/identity-providers")
	if err != nil {
		return nil, err
	}
	return *l, nil
}

func (c *Client) IdentityProvider(ctx context.Context, id string) (*IdentityProvider, error) {
	return get[IdentityProvider](ctx, c, p("/identity-providers/%s", id))
}

func (c *Client) CreateIdentityProvider(ctx context.Context, body map[string]any) (*IdentityProvider, error) {
	return send[IdentityProvider](ctx, c, http.MethodPost, "/identity-providers", body)
}

func (c *Client) UpdateIdentityProvider(ctx context.Context, id string, body map[string]any) (*IdentityProvider, error) {
	return send[IdentityProvider](ctx, c, http.MethodPatch, p("/identity-providers/%s", id), body)
}

func (c *Client) DeleteIdentityProvider(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, p("/identity-providers/%s", id), nil, nil)
}

type DomainChallenge struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Host  string `json:"host"`
	Value string `json:"value"`
}

type Domain struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Verified   bool            `json:"verified"`
	VerifiedAt *string         `json:"verified_at"`
	ExpiresAt  *string         `json:"expires_at"`
	Challenge  DomainChallenge `json:"challenge"`
}

func (c *Client) Domains(ctx context.Context) ([]Domain, error) {
	l, err := get[[]Domain](ctx, c, "/domains")
	if err != nil {
		return nil, err
	}
	return *l, nil
}

func (c *Client) Domain(ctx context.Context, id string) (*Domain, error) {
	return get[Domain](ctx, c, p("/domains/%s", id))
}

func (c *Client) CreateDomain(ctx context.Context, name string) (*Domain, error) {
	return send[Domain](ctx, c, http.MethodPost, "/domains", map[string]any{"name": name})
}

func (c *Client) VerifyDomain(ctx context.Context, id string) (*Domain, error) {
	return send[Domain](ctx, c, http.MethodPost, p("/domains/%s/verify", id), nil)
}

func (c *Client) DeleteDomain(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, p("/domains/%s", id), nil, nil)
}

type ServiceToken struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	CertSHA256     *string  `json:"cert_sha256"`
	CertExpiresAt  *string  `json:"cert_expires_at"`
	ExpiresAt      *string  `json:"expires_at"`
	Expired        bool     `json:"expired"`
	Apps           []string `json:"apps"`
	CreatedAt      *string  `json:"created_at"`
	Secret         *string  `json:"secret,omitempty"`
	Certificate    *string  `json:"certificate,omitempty"`
	PrivateKey     *string  `json:"private_key,omitempty"`
	PKCS12         *string  `json:"pkcs12,omitempty"`
	PKCS12Password *string  `json:"pkcs12_password,omitempty"`
}

func (c *Client) ServiceToken(ctx context.Context, id string) (*ServiceToken, error) {
	return get[ServiceToken](ctx, c, p("/service-tokens/%s", id))
}

func (c *Client) CreateServiceToken(ctx context.Context, body map[string]any) (*ServiceToken, error) {
	return send[ServiceToken](ctx, c, http.MethodPost, "/service-tokens", body)
}

func (c *Client) DeleteServiceToken(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, p("/service-tokens/%s", id), nil, nil)
}

type LogDestination struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Name          string         `json:"name"`
	Streams       []string       `json:"streams"`
	Enabled       bool           `json:"enabled"`
	Config        map[string]any `json:"config"`
	CaSHA256      *string        `json:"ca_sha256"`
	HasAuthHeader bool           `json:"has_auth_header"`
	HasSecretKey  bool           `json:"has_secret_key"`
	Status        string         `json:"status"`
	SigningSecret *string        `json:"signing_secret,omitempty"`
}

func (c *Client) LogDestination(ctx context.Context, id string) (*LogDestination, error) {
	return get[LogDestination](ctx, c, p("/log-destinations/%s", id))
}

func (c *Client) CreateLogDestination(ctx context.Context, body map[string]any) (*LogDestination, error) {
	return send[LogDestination](ctx, c, http.MethodPost, "/log-destinations", body)
}

func (c *Client) UpdateLogDestination(ctx context.Context, id string, body map[string]any) (*LogDestination, error) {
	return send[LogDestination](ctx, c, http.MethodPatch, p("/log-destinations/%s", id), body)
}

func (c *Client) DeleteLogDestination(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, p("/log-destinations/%s", id), nil, nil)
}

// Branding is GET /workspace/branding (not wrapped in data).
type Branding struct {
	Allowed bool    `json:"allowed"`
	Accent  *string `json:"accent"`
	Logo    *string `json:"logo"`
}

func (c *Client) Branding(ctx context.Context) (*Branding, error) {
	var b Branding
	if err := c.Do(ctx, http.MethodGet, "/workspace/branding", nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (c *Client) SetBranding(ctx context.Context, body map[string]any) (*Branding, error) {
	var b Branding
	if err := c.Do(ctx, http.MethodPut, "/workspace/branding", body, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (c *Client) DeleteBranding(ctx context.Context) error {
	return c.Do(ctx, http.MethodDelete, "/workspace/branding", nil, nil)
}
