// Package fakeapi is an in-memory stand-in for the Codigate workspace API (/api/v1), for the
// provider's unit tests and local end-to-end runs. It mirrors the shapes, status codes and
// normalizations of control-plane/app/Http/Controllers/Api, not every validation rule.
package fakeapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

// API is the fake. AdminToken may read and write, ViewerToken may only read.
type API struct {
	AdminToken  string
	ViewerToken string
	// AppDomain is the workspace's app domain; app hostnames under it are live without a verified domain.
	AppDomain string
	// Preverified lists domains that count as verified without being in the API, like ones verified
	// before Terraform managed the workspace. Public apps and bypass paths need one, as in the real API.
	Preverified []string

	mu       sync.Mutex
	seq      int
	agents   map[string]*client.Agent
	apps     map[string]*client.App
	edge     map[string]client.EdgeRules
	idps     map[string]*idp
	domains  map[string]*client.Domain
	tokens   map[string]*client.ServiceToken
	logs     map[string]*logDest
	branding client.Branding
	requests []string
}

type idp struct {
	client.IdentityProvider
	secret string
}

type logDest struct {
	client.LogDestination
	authHeader, secretKey string
}

// New returns an empty fake for one workspace.
func New(adminToken, viewerToken string) *API {
	return &API{
		AdminToken: adminToken, ViewerToken: viewerToken, AppDomain: "acme.codigate.app",
		agents: map[string]*client.Agent{}, apps: map[string]*client.App{}, edge: map[string]client.EdgeRules{},
		idps: map[string]*idp{}, domains: map[string]*client.Domain{}, tokens: map[string]*client.ServiceToken{},
		logs: map[string]*logDest{}, branding: client.Branding{Allowed: true},
	}
}

// Start serves the fake on a local httptest server; the API base URL is server.URL + "/api/v1".
func (a *API) Start() *httptest.Server { return httptest.NewServer(a) }

// Requests lists "METHOD /path" of every request so far.
func (a *API) Requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

// Remove deletes an object by id behind Terraform's back, to simulate drift. The id
// "branding" resets the workspace branding.
func (a *API) Remove(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == "branding" {
		a.branding.Accent, a.branding.Logo = nil, nil
		return
	}
	delete(a.agents, id)
	delete(a.apps, id)
	delete(a.edge, id)
	delete(a.idps, id)
	delete(a.domains, id)
	delete(a.tokens, id)
	delete(a.logs, id)
}

// ClearEdgeRules resets an app's edge rules behind Terraform's back.
func (a *API) ClearEdgeRules(appID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.edge, appID)
}

// SetAgentConnected marks an agent as connected, as a running agent would.
func (a *API) SetAgentConnected(id string, connected bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ag, ok := a.agents[id]; ok {
		ag.Connected = connected
	}
}

// IDs returns the ids of every stored object, for assertions.
func (a *API) IDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, m := range []func() []string{
		func() []string { return keys(a.agents) }, func() []string { return keys(a.apps) },
		func() []string { return keys(a.idps) }, func() []string { return keys(a.domains) },
		func() []string { return keys(a.tokens) }, func() []string { return keys(a.logs) },
	} {
		out = append(out, m()...)
	}
	sort.Strings(out)
	return out
}

func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

type apiError struct {
	status int
	msg    string
	errors map[string][]string
}

func notFound() *apiError { return &apiError{status: 404, msg: "Not found."} }

func invalid(field, msg string) *apiError {
	return &apiError{status: 422, msg: msg, errors: map[string][]string{field: {msg}}}
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case auth == a.AdminToken && auth != "":
	case auth == a.ViewerToken && auth != "":
		if r.Method != http.MethodGet {
			writeError(w, &apiError{status: 403, msg: "This token can only read."})
			return
		}
	default:
		writeError(w, &apiError{status: 401, msg: "Unauthenticated."})
		return
	}
	route, ok := strings.CutPrefix(r.URL.Path, "/api/v1")
	if !ok {
		writeError(w, notFound())
		return
	}
	var body map[string]any
	if r.Body != nil && r.Method != http.MethodGet {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body == nil {
		body = map[string]any{}
	}
	status, out, err := a.route(r.Method, strings.Split(strings.Trim(route, "/"), "/"), body)
	if err != nil {
		writeError(w, err)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

func writeError(w http.ResponseWriter, e *apiError) {
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": e.msg, "errors": e.errors})
}

func data(v any) map[string]any { return map[string]any{"data": v} }

func (a *API) route(method string, seg []string, b map[string]any) (int, any, *apiError) {
	key := method + " " + seg[0]
	switch len(seg) {
	case 1:
		switch key {
		case "GET workspace":
			return 200, map[string]any{"id": "ws_fake", "name": "Acme", "slug": "acme", "app_domain": a.AppDomain, "plan": "business", "suspended": false}, nil
		case "GET agents":
			return 200, data(sorted(a.agents, func(x *client.Agent) string { return x.Name })), nil
		case "POST agents":
			return a.createAgent(b)
		case "GET apps":
			list := sorted(a.apps, func(x *client.App) string { return x.Hostname })
			for _, x := range list {
				a.refreshApp(x)
			}
			return 200, data(list), nil
		case "POST apps":
			return a.createApp(b)
		case "GET identity-providers":
			return 200, data(sorted(a.idps, func(x *idp) string { return x.Name })), nil
		case "POST identity-providers":
			return a.saveIdp(nil, b)
		case "GET domains":
			return 200, data(sorted(a.domains, func(x *client.Domain) string { return x.Name })), nil
		case "POST domains":
			return a.createDomain(b)
		case "GET service-tokens":
			return 200, data(sorted(a.tokens, func(x *client.ServiceToken) string { return x.Name })), nil
		case "POST service-tokens":
			return a.createToken(b)
		case "GET log-destinations":
			return 200, data(sorted(a.logs, func(x *logDest) string { return x.Name })), nil
		case "POST log-destinations":
			return a.saveLog(nil, b)
		}
	case 2:
		id := seg[1]
		switch key {
		case "GET workspace", "PUT workspace", "DELETE workspace":
			if id == "branding" {
				return a.brandingRoute(method, b)
			}
		case "GET agents", "PATCH agents", "DELETE agents":
			ag, ok := a.agents[id]
			if !ok {
				return 0, nil, notFound()
			}
			switch method {
			case "GET":
				return 200, data(ag), nil
			case "PATCH":
				name, e := str(b, "name", true, 100)
				if e != nil {
					return 0, nil, e
				}
				ag.Name = name
				return 200, data(ag), nil
			}
			for _, x := range a.apps {
				if x.AgentID != nil && *x.AgentID == id {
					return 0, nil, invalid("agent", "Delete or move the apps that use this agent first.")
				}
			}
			delete(a.agents, id)
			return 0, nil, nil
		case "GET apps", "PATCH apps", "DELETE apps":
			x, ok := a.apps[id]
			if !ok {
				return 0, nil, notFound()
			}
			switch method {
			case "GET":
				a.refreshApp(x)
				return 200, data(x), nil
			case "PATCH":
				return a.updateApp(x, b)
			}
			delete(a.apps, id)
			delete(a.edge, id)
			return 0, nil, nil
		case "GET identity-providers", "PATCH identity-providers", "DELETE identity-providers":
			p, ok := a.idps[id]
			if !ok {
				return 0, nil, notFound()
			}
			switch method {
			case "GET":
				return 200, data(p), nil
			case "PATCH":
				return a.saveIdp(p, b)
			}
			for _, x := range a.apps {
				if x.IdpID != nil && *x.IdpID == id {
					return 0, nil, invalid("identity_provider", "Apps still log in with this identity provider.")
				}
			}
			delete(a.idps, id)
			return 0, nil, nil
		case "GET domains", "DELETE domains":
			d, ok := a.domains[id]
			if !ok {
				return 0, nil, notFound()
			}
			if method == "GET" {
				return 200, data(d), nil
			}
			delete(a.domains, id)
			return 0, nil, nil
		case "GET service-tokens", "DELETE service-tokens":
			t, ok := a.tokens[id]
			if !ok {
				return 0, nil, notFound()
			}
			if method == "GET" {
				return 200, data(a.presentToken(t)), nil
			}
			delete(a.tokens, id)
			for _, x := range a.apps {
				x.ServiceTokens = without(x.ServiceTokens, id)
			}
			return 0, nil, nil
		case "GET log-destinations", "PATCH log-destinations", "DELETE log-destinations":
			l, ok := a.logs[id]
			if !ok {
				return 0, nil, notFound()
			}
			switch method {
			case "GET":
				return 200, data(l.LogDestination), nil
			case "PATCH":
				return a.saveLog(l, b)
			}
			delete(a.logs, id)
			return 0, nil, nil
		}
	case 3:
		id, sub := seg[1], seg[2]
		switch key + "/" + sub {
		case "POST agents/rotate":
			ag, ok := a.agents[id]
			if !ok {
				return 0, nil, notFound()
			}
			return 200, map[string]any{"data": ag, "token": a.secret("cga_")}, nil
		case "PUT apps/access", "PUT apps/enabled", "GET apps/edge-rules", "PUT apps/edge-rules":
			x, ok := a.apps[id]
			if !ok {
				return 0, nil, notFound()
			}
			switch sub {
			case "access":
				return a.setAccess(x, b)
			case "enabled":
				en, _ := b["enabled"].(bool)
				x.Enabled = en
				a.refreshApp(x)
				return 200, data(x), nil
			}
			if method == "PUT" {
				var rules client.EdgeRules
				raw, _ := json.Marshal(b)
				if err := json.Unmarshal(raw, &rules); err != nil {
					return 0, nil, invalid("rules", "Invalid edge rules.")
				}
				a.edge[id] = rules
			}
			return 200, data(a.edgeView(id)), nil
		case "POST domains/verify":
			d, ok := a.domains[id]
			if !ok {
				return 0, nil, notFound()
			}
			if !strings.HasSuffix(d.Name, ".verified.test") {
				return 0, nil, invalid("domain", "No TXT record "+d.Challenge.Name+" with the expected value yet.")
			}
			now := stamp()
			d.Verified, d.VerifiedAt, d.ExpiresAt = true, &now, nil
			return 200, data(d), nil
		}
	}
	return 0, nil, notFound()
}

func (a *API) nextID(prefix string) string {
	a.seq++
	return fmt.Sprintf("%s%06d", prefix, a.seq)
}

func (a *API) secret(prefix string) string {
	a.seq++
	sum := sha256.Sum256([]byte(fmt.Sprint(prefix, a.seq)))
	return prefix + hex.EncodeToString(sum[:16])
}

func stamp() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func ptr(s string) *string { return &s }

func sorted[T any](m map[string]T, by func(T) string) []T {
	out := make([]T, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return by(out[i]) < by(out[j]) })
	return out
}

func without(list []string, v string) []string {
	out := []string{}
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func str(b map[string]any, key string, required bool, max int) (string, *apiError) {
	v, ok := b[key]
	if !ok || v == nil {
		if required {
			return "", invalid(key, "The "+key+" field is required.")
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", invalid(key, "The "+key+" field must be a string.")
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", invalid(key, "The "+key+" field is required.")
	}
	if max > 0 && len(s) > max {
		return "", invalid(key, fmt.Sprintf("The %s field must not be greater than %d characters.", key, max))
	}
	return s, nil
}

func strs(b map[string]any, key string) []string {
	out := []string{}
	l, _ := b[key].([]any)
	for _, v := range l {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (a *API) createAgent(b map[string]any) (int, any, *apiError) {
	name, e := str(b, "name", true, 100)
	if e != nil {
		return 0, nil, e
	}
	ag := &client.Agent{ID: a.nextID("agt_"), Name: name, CreatedAt: ptr(stamp())}
	a.agents[ag.ID] = ag
	return 201, map[string]any{"data": ag, "token": a.secret("cga_")}, nil
}

func (a *API) hostname(b map[string]any) (string, *apiError) {
	h, e := str(b, "hostname", true, 253)
	if e != nil {
		return "", e
	}
	h = client.NormalizeHostname(h)
	if !strings.Contains(h, ".") {
		return "", invalid("hostname", "Enter a full hostname, for example app.example.com.")
	}
	return h, nil
}

func (a *API) hostnameTaken(h, except string) bool {
	for _, x := range a.apps {
		if x.Hostname == h && x.ID != except {
			return true
		}
	}
	return false
}

func (a *API) createApp(b map[string]any) (int, any, *apiError) {
	name, e := str(b, "name", true, 100)
	if e != nil {
		return 0, nil, e
	}
	h, e := a.hostname(b)
	if e != nil {
		return 0, nil, e
	}
	if a.hostnameTaken(h, "") {
		return 0, nil, invalid("hostname", "This hostname is already in use.")
	}
	agentID, e := str(b, "agent_id", true, 0)
	if e != nil {
		return 0, nil, e
	}
	if _, ok := a.agents[agentID]; !ok {
		return 0, nil, invalid("agent_id", "Unknown agent.")
	}
	x := &client.App{ID: a.nextID("app_"), Name: name, Hostname: h, AgentID: &agentID, Access: "protected",
		ServiceTokens: []string{}, BypassPaths: []string{}, Enabled: true, CreatedAt: ptr(stamp())}
	a.apps[x.ID] = x
	a.refreshApp(x)
	return 201, data(x), nil
}

func (a *API) updateApp(x *client.App, b map[string]any) (int, any, *apiError) {
	if _, ok := b["name"]; ok {
		name, e := str(b, "name", true, 100)
		if e != nil {
			return 0, nil, e
		}
		x.Name = name
	}
	if _, ok := b["hostname"]; ok {
		h, e := a.hostname(b)
		if e != nil {
			return 0, nil, e
		}
		if a.hostnameTaken(h, x.ID) {
			return 0, nil, invalid("hostname", "This hostname is already in use.")
		}
		x.Hostname = h
	}
	if _, ok := b["agent_id"]; ok {
		id, e := str(b, "agent_id", true, 0)
		if e != nil {
			return 0, nil, e
		}
		if _, ok := a.agents[id]; !ok {
			return 0, nil, invalid("agent_id", "Unknown agent.")
		}
		x.AgentID = &id
	}
	a.refreshApp(x)
	return 200, data(x), nil
}

func (a *API) setAccess(x *client.App, b map[string]any) (int, any, *apiError) {
	access, _ := b["access"].(string)
	switch access {
	case "public":
		if !a.verifiedDomainCovers(x.Hostname) {
			return 0, nil, invalid("access", "Public apps need a verified domain that covers "+x.Hostname+".")
		}
		x.Access, x.IdpID, x.Policy, x.ServiceTokens, x.BypassPaths = "public", nil, nil, []string{}, []string{}
	case "protected":
		idpID, _ := b["idp_id"].(string)
		if _, ok := a.idps[idpID]; !ok {
			return 0, nil, invalid("identity_provider", "Unknown identity provider.")
		}
		raw, _ := json.Marshal(b["policy"])
		var pol client.Policy
		if err := json.Unmarshal(raw, &pol); err != nil || b["policy"] == nil {
			return 0, nil, invalid("policy", "The policy field is required when access is protected.")
		}
		pol.Emails, pol.EmailDomains, pol.Groups = lowerSorted(pol.Emails), lowerSorted(pol.EmailDomains), uniqueSorted(pol.Groups)
		if !pol.Everyone && len(pol.Emails)+len(pol.EmailDomains)+len(pol.Groups) == 0 {
			return 0, nil, invalid("policy", "The policy lets nobody in.")
		}
		paths := uniqueSorted(strs(b, "bypass_paths"))
		if len(paths) > 0 && !a.verifiedDomainCovers(x.Hostname) {
			return 0, nil, invalid("access", "Bypass paths need a verified domain that covers "+x.Hostname+".")
		}
		x.Access, x.IdpID, x.Policy = "protected", &idpID, &pol
		if _, ok := b["service_tokens"]; ok {
			ids := uniqueSorted(strs(b, "service_tokens"))
			for _, id := range ids {
				if _, ok := a.tokens[id]; !ok {
					return 0, nil, invalid("service_tokens", "Unknown service token: "+id+".")
				}
			}
			x.ServiceTokens = ids
		}
		if _, ok := b["bypass_paths"]; ok {
			x.BypassPaths = paths
		}
	default:
		return 0, nil, invalid("access", "The selected access is invalid.")
	}
	a.refreshApp(x)
	return 200, data(x), nil
}

func lowerSorted(l []string) []string {
	for i := range l {
		l[i] = strings.ToLower(strings.TrimSpace(l[i]))
	}
	return uniqueSorted(l)
}

func uniqueSorted(l []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range l {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func (a *API) verifiedDomainCovers(h string) bool {
	covers := func(name string) bool { return h == name || strings.HasSuffix(h, "."+name) }
	for _, name := range a.Preverified {
		if covers(name) {
			return true
		}
	}
	for _, d := range a.domains {
		if d.Verified && covers(d.Name) {
			return true
		}
	}
	return false
}

func (a *API) refreshApp(x *client.App) {
	covered := strings.HasSuffix(x.Hostname, "."+a.AppDomain) || a.verifiedDomainCovers(x.Hostname)
	var reason string
	switch {
	case !x.Enabled:
		reason = "switched off"
	case !covered:
		reason = "needs a verified domain"
	case x.Access != "public" && (x.IdpID == nil || x.Policy == nil):
		reason = "needs access settings"
	}
	x.Live = reason == ""
	x.NotLiveReason = nil
	if reason != "" {
		x.NotLiveReason = &reason
	}
}

func (a *API) edgeView(appID string) client.EdgeRulesView {
	r := a.edge[appID]
	for _, l := range []*[]string{&r.AllowIPs, &r.DenyIPs, &r.AllowCountries, &r.DenyCountries} {
		if *l == nil {
			*l = []string{}
		}
	}
	if r.Paths == nil {
		r.Paths = []client.PathRule{}
	}
	if r.Waf == nil || r.Waf.Mode == "" {
		r.Waf = &client.Waf{Mode: "off"}
	}
	if r.Waf.Exclusions == nil {
		r.Waf.Exclusions = []int64{}
	}
	return client.EdgeRulesView{AppID: appID, Rules: r, AdvancedAllowed: true, Inactive: []string{}}
}

func (a *API) saveIdp(p *idp, b map[string]any) (int, any, *apiError) {
	creating := p == nil
	if creating {
		typ := "oidc"
		if t, ok := b["type"].(string); ok && t != "" {
			typ = t
		}
		if typ != "oidc" && typ != "email" {
			return 0, nil, invalid("type", "The selected type is invalid.")
		}
		p = &idp{IdentityProvider: client.IdentityProvider{Type: typ, Scopes: []string{}}}
	}
	if _, ok := b["name"]; ok || creating {
		name, e := str(b, "name", true, 100)
		if e != nil {
			return 0, nil, e
		}
		p.Name = name
	}
	if p.Type == "oidc" {
		if _, ok := b["issuer"]; ok || creating {
			iss, e := str(b, "issuer", true, 255)
			if e != nil {
				return 0, nil, e
			}
			if !strings.HasPrefix(iss, "https://") {
				return 0, nil, invalid("issuer", "The issuer must be an https URL.")
			}
			p.Issuer = strings.TrimRight(iss, "/")
		}
		if _, ok := b["client_id"]; ok || creating {
			cid, e := str(b, "client_id", true, 255)
			if e != nil {
				return 0, nil, e
			}
			p.ClientID = cid
		}
		if s, _ := b["client_secret"].(string); s != "" {
			p.secret = s
		}
		if rm, _ := b["remove_client_secret"].(bool); rm {
			p.secret = ""
		}
		if _, ok := b["scopes"]; ok {
			p.Scopes = strs(b, "scopes")
		}
		if _, ok := b["groups_claim"]; ok {
			g, _ := str(b, "groups_claim", false, 64)
			p.GroupsClaim = g
		}
		if p.GroupsClaim == "" {
			p.GroupsClaim = "groups"
		}
		p.RedirectURI = "https://auth.codigate.test/oidc/callback"
	}
	p.HasClientSecret = p.secret != ""
	if creating {
		p.ID = a.nextID("idp_")
		a.idps[p.ID] = p
		return 201, data(p), nil
	}
	return 200, data(p), nil
}

func (a *API) createDomain(b map[string]any) (int, any, *apiError) {
	name, e := str(b, "name", true, 253)
	if e != nil {
		return 0, nil, e
	}
	name = client.NormalizeHostname(name)
	for _, d := range a.domains {
		if d.Name == name {
			return 0, nil, invalid("name", "This domain is already added.")
		}
	}
	expires := time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02T15:04:05Z")
	d := &client.Domain{ID: a.nextID("dom_"), Name: name, ExpiresAt: &expires, Challenge: client.DomainChallenge{
		Type: "TXT", Name: "_codigate-challenge." + name, Host: "_codigate-challenge", Value: a.secret("codigate-verify="),
	}}
	a.domains[d.ID] = d
	return 201, data(d), nil
}

func (a *API) presentToken(t *client.ServiceToken) client.ServiceToken {
	out := *t
	out.Secret, out.Certificate, out.PrivateKey, out.PKCS12, out.PKCS12Password = nil, nil, nil, nil, nil
	out.Apps = []string{}
	for _, x := range sorted(a.apps, func(x *client.App) string { return x.Hostname }) {
		for _, id := range x.ServiceTokens {
			if id == t.ID {
				out.Apps = append(out.Apps, x.ID)
			}
		}
	}
	return out
}

func (a *API) createToken(b map[string]any) (int, any, *apiError) {
	name, e := str(b, "name", true, 100)
	if e != nil {
		return 0, nil, e
	}
	typ := "secret"
	if s, ok := b["type"].(string); ok && s != "" {
		typ = s
	}
	t := &client.ServiceToken{ID: a.nextID("svc_"), Name: name, Type: typ, Apps: []string{}, CreatedAt: ptr(stamp())}
	if d, ok := b["expires_in_days"].(float64); ok {
		if d < 1 || d > 3650 {
			return 0, nil, invalid("expires_in_days", "The expires in days field must be between 1 and 3650.")
		}
		t.ExpiresAt = ptr(time.Now().UTC().AddDate(0, 0, int(d)).Format("2006-01-02T15:04:05Z"))
	}
	out := *t
	switch typ {
	case "secret":
		out.Secret = ptr(a.secret("cgs_"))
	case "certificate":
		pem, _ := b["certificate"].(string)
		if pem == "" {
			pem = "-----BEGIN CERTIFICATE-----\n" + a.secret("") + "\n-----END CERTIFICATE-----"
			out.PrivateKey = ptr("-----BEGIN PRIVATE KEY-----\n" + a.secret("") + "\n-----END PRIVATE KEY-----")
			out.PKCS12 = ptr(base64.StdEncoding.EncodeToString([]byte(a.secret("p12"))))
			out.PKCS12Password = ptr(a.secret(""))
		}
		sum := sha256.Sum256([]byte(strings.TrimSpace(pem)))
		fp := hex.EncodeToString(sum[:])
		for _, o := range a.tokens {
			if o.CertSHA256 != nil && *o.CertSHA256 == fp {
				return 0, nil, invalid("certificate", "Another service token already uses this certificate.")
			}
		}
		exp := time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02T15:04:05Z")
		t.CertSHA256, t.CertExpiresAt = &fp, &exp
		out.CertSHA256, out.CertExpiresAt = &fp, &exp
		out.Certificate = &pem
	default:
		return 0, nil, invalid("type", "The selected type is invalid.")
	}
	a.tokens[t.ID] = t
	return 201, data(out), nil
}

var logFields = map[string][]string{
	"https":  {"url"},
	"syslog": {"host", "port", "app_name"},
	"s3":     {"endpoint", "region", "bucket", "prefix", "access_key"},
}

func (a *API) saveLog(l *logDest, b map[string]any) (int, any, *apiError) {
	if s, _ := b["auth_header"].(string); s != "" && !authHeaderRe.MatchString(s) {
		return 0, nil, invalid("auth_header", `Use the form "Header-Name: value" on one line.`)
	}
	creating := l == nil
	if creating {
		typ, _ := b["type"].(string)
		if _, ok := logFields[typ]; !ok {
			return 0, nil, invalid("type", "The selected type is invalid.")
		}
		l = &logDest{LogDestination: client.LogDestination{Type: typ, Enabled: true, Config: map[string]any{}, Status: "ok"}}
	}
	if _, ok := b["name"]; ok || creating {
		name, e := str(b, "name", true, 100)
		if e != nil {
			return 0, nil, e
		}
		l.Name = name
	}
	if _, ok := b["streams"]; ok || creating {
		streams := uniqueSorted(strs(b, "streams"))
		if len(streams) == 0 {
			return 0, nil, invalid("streams", "The streams field is required.")
		}
		for _, s := range streams {
			if s != "access" && s != "audit" {
				return 0, nil, invalid("streams", "The selected stream is invalid.")
			}
		}
		l.Streams = streams
	}
	if en, ok := b["enabled"].(bool); ok {
		l.Enabled = en
	}
	cfg := map[string]any{}
	for _, f := range logFields[l.Type] {
		v, present := b[f]
		if !present {
			v = l.Config[f]
		}
		switch x := v.(type) {
		case string:
			if s := strings.TrimSpace(x); s != "" {
				cfg[f] = s
			}
		case float64:
			cfg[f] = x
		}
	}
	if l.Type == "syslog" {
		if _, ok := cfg["port"]; !ok {
			cfg["port"] = float64(6514)
		}
	}
	required := map[string][]string{"https": {"url"}, "syslog": {"host"}, "s3": {"endpoint", "region", "bucket", "access_key"}}
	for _, f := range required[l.Type] {
		if _, ok := cfg[f]; !ok {
			return 0, nil, invalid(f, "The "+f+" field is required.")
		}
	}
	l.Config = cfg
	if s, _ := b["auth_header"].(string); s != "" {
		l.authHeader = s
	}
	if c, _ := b["clear_auth_header"].(bool); c {
		l.authHeader = ""
	}
	if s, _ := b["secret_key"].(string); s != "" {
		l.secretKey = s
	}
	if l.Type == "s3" && l.secretKey == "" {
		return 0, nil, invalid("secret_key", "The secret key field is required.")
	}
	if v, ok := b["ca_pem"]; ok {
		pem, _ := v.(string)
		if pem = strings.TrimSpace(pem); pem == "" {
			l.CaSHA256 = nil
		} else {
			sum := sha256.Sum256([]byte(pem))
			l.CaSHA256 = ptr(hex.EncodeToString(sum[:]))
		}
	}
	l.HasAuthHeader, l.HasSecretKey = l.authHeader != "", l.secretKey != ""
	if !creating {
		return 200, data(l.LogDestination), nil
	}
	l.ID = a.nextID("ldst_")
	a.logs[l.ID] = l
	out := l.LogDestination
	if l.Type == "https" {
		out.SigningSecret = ptr(a.secret("whsec_"))
	}
	return 201, data(out), nil
}

func (a *API) brandingRoute(method string, b map[string]any) (int, any, *apiError) {
	switch method {
	case "GET":
		return 200, a.branding, nil
	case "DELETE":
		a.branding.Accent, a.branding.Logo = nil, nil
		return 0, nil, nil
	}
	if v, ok := b["accent"]; ok {
		if s, _ := v.(string); s == "" {
			a.branding.Accent = nil
		} else {
			s = strings.ToLower(s)
			if len(s) != 7 || s[0] != '#' {
				return 0, nil, invalid("accent", "The accent must be a #rrggbb color.")
			}
			a.branding.Accent = &s
		}
	}
	if s, _ := b["logo"].(string); s != "" {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(raw) == 0 {
			return 0, nil, invalid("logo", "The logo must be a base64 PNG, JPEG, SVG or WebP image.")
		}
		mime := http.DetectContentType(raw)
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "<svg") {
			mime = "image/svg+xml"
		}
		a.branding.Logo = ptr("data:" + strings.Split(mime, ";")[0] + ";base64," + base64.StdEncoding.EncodeToString(raw))
	}
	if rm, _ := b["remove_logo"].(bool); rm {
		a.branding.Logo = nil
	}
	return 200, a.branding, nil
}

var authHeaderRe = regexp.MustCompile(`^[A-Za-z0-9-]+:[ \t]*\S[^\r\n]*$`)
