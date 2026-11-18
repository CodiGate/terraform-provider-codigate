package provider

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/codigate/terraform-provider-codigate/internal/fakeapi"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	testAdminToken  = "cgt_test_admin"
	testViewerToken = "cgt_test_viewer"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){
	"codigate": providerserver.NewProtocol6WithError(New("test")()),
}

// fake starts an in-memory API for one test and returns it with the provider block that points at it.
func fake(t *testing.T) (*fakeapi.API, string) {
	t.Helper()
	api := fakeapi.New(testAdminToken, testViewerToken)
	srv := api.Start()
	t.Cleanup(srv.Close)
	return api, fmt.Sprintf("provider \"codigate\" {\n  api_url = %q\n  token   = %q\n}\n", srv.URL+"/api/v1", testAdminToken)
}

// idOf reads the id of a resource in state, for drift helpers that run between steps.
func idOf(s *terraform.State, addr string) string {
	if s == nil {
		return ""
	}
	if r, ok := s.RootModule().Resources[addr]; ok {
		return r.Primary.ID
	}
	return ""
}

// removeOutside deletes the object behind addr in the fake, as someone in the dashboard would.
// last is read when the step runs, not when the test case is built.
func removeOutside(api *fakeapi.API, addr string, last **terraform.State) func() {
	return func() { api.Remove(idOf(*last, addr)) }
}

// stateCapture remembers the state after a step so a later PreConfig can find ids.
func stateCapture(dst **terraform.State) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		*dst = s
		return nil
	}
}

func matches(re string) *regexp.Regexp { return regexp.MustCompile(re) }

func TestProviderNeedsToken(t *testing.T) {
	t.Setenv("CODIGATE_API_TOKEN", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      `data "codigate_workspace" "this" {}`,
			ExpectError: matches(`CODIGATE_API_TOKEN`),
		}},
	})
}

func TestProviderReadsEnvironment(t *testing.T) {
	api := fakeapi.New(testAdminToken, testViewerToken)
	srv := api.Start()
	defer srv.Close()
	t.Setenv("CODIGATE_API_URL", srv.URL+"/api/v1")
	t.Setenv("CODIGATE_API_TOKEN", testViewerToken)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: `data "codigate_workspace" "this" {}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.codigate_workspace.this", "id", "ws_fake"),
				resource.TestCheckResourceAttr("data.codigate_workspace.this", "slug", "acme"),
				resource.TestCheckResourceAttr("data.codigate_workspace.this", "app_domain", "acme.codigate.app"),
				resource.TestCheckResourceAttr("data.codigate_workspace.this", "plan", "business"),
			),
		}},
	})
}

func TestViewerTokenCannotWrite(t *testing.T) {
	api := fakeapi.New(testAdminToken, testViewerToken)
	srv := api.Start()
	defer srv.Close()
	cfg := fmt.Sprintf("provider \"codigate\" {\n  api_url = %q\n  token = %q\n}\n", srv.URL+"/api/v1", testViewerToken)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      cfg + `resource "codigate_agent" "a" { name = "x" }`,
			ExpectError: matches(`can only read`),
		}},
	})
}

func TestAgentResource(t *testing.T) {
	api, p := fake(t)
	var last *terraform.State
	var firstToken string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `resource "codigate_agent" "a" { name = "home-lab" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_agent.a", "id", matches(`^agt_`)),
					resource.TestCheckResourceAttr("codigate_agent.a", "name", "home-lab"),
					resource.TestMatchResourceAttr("codigate_agent.a", "token", matches(`^cga_`)),
					resource.TestCheckResourceAttrSet("codigate_agent.a", "created_at"),
					func(s *terraform.State) error {
						firstToken = s.RootModule().Resources["codigate_agent.a"].Primary.Attributes["token"]
						return nil
					},
					stateCapture(&last),
				),
			},
			{
				// Renaming keeps the agent and its token.
				Config: p + `resource "codigate_agent" "a" { name = "home-lab-2" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_agent.a", "name", "home-lab-2"),
					func(s *terraform.State) error {
						if idOf(s, "codigate_agent.a") != idOf(last, "codigate_agent.a") {
							return fmt.Errorf("rename replaced the agent")
						}
						if got := s.RootModule().Resources["codigate_agent.a"].Primary.Attributes["token"]; got != firstToken {
							return fmt.Errorf("rename changed the token")
						}
						return nil
					},
				),
			},
			{
				// Changing rotate_trigger rotates the token in place.
				Config: p + `resource "codigate_agent" "a" {
  name           = "home-lab-2"
  rotate_trigger = "2026-01"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_agent.a", "rotate_trigger", "2026-01"),
					func(s *terraform.State) error {
						if idOf(s, "codigate_agent.a") != idOf(last, "codigate_agent.a") {
							return fmt.Errorf("rotate replaced the agent")
						}
						got := s.RootModule().Resources["codigate_agent.a"].Primary.Attributes["token"]
						if got == firstToken || !strings.HasPrefix(got, "cga_") {
							return fmt.Errorf("rotate did not issue a new token: %q", got)
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "codigate_agent.a",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token", "rotate_trigger"},
			},
			{
				// Deleted in the dashboard: the next refresh drops it and plans a create.
				PreConfig:          removeOutside(api, "codigate_agent.a", &last),
				Config:             p + `resource "codigate_agent" "a" { name = "home-lab-2" }`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if ids := api.IDs(); len(ids) != 0 {
				return fmt.Errorf("left behind: %v", ids)
			}
			return nil
		},
	})
}

func rotations(api *fakeapi.API) int {
	n := 0
	for _, r := range api.Requests() {
		if strings.HasSuffix(r, "/rotate") {
			n++
		}
	}
	return n
}

// An imported agent adopts the configured rotate_trigger without rotating its token, so importing
// a running agent doesn't disconnect it; a later change of the trigger still rotates.
func TestAgentImportAdoptsRotateTrigger(t *testing.T) {
	api, p := fake(t)
	var last *terraform.State
	cfg := func(trigger string) string {
		return p + fmt.Sprintf("resource \"codigate_agent\" \"a\" {\n  name           = \"home-lab\"\n  rotate_trigger = %q\n}\n", trigger)
	}
	noRotation := func(*terraform.State) error {
		if n := rotations(api); n != 0 {
			return fmt.Errorf("rotated %d times", n)
		}
		return nil
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: cfg("r1"), Check: resource.ComposeAggregateTestCheckFunc(noRotation, stateCapture(&last))},
			{
				// Forget the agent without deleting it, then import it as an existing agent.
				Config: p + "removed {\n  from = codigate_agent.a\n  lifecycle {\n    destroy = false\n  }\n}\n",
			},
			{
				Config:             cfg("r1"),
				ResourceName:       "codigate_agent.a",
				ImportState:        true,
				ImportStatePersist: true,
				ImportStateIdFunc:  func(*terraform.State) (string, error) { return idOf(last, "codigate_agent.a"), nil },
			},
			{
				Config: cfg("r1"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("codigate_agent.a", plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue("codigate_agent.a", tfjsonpath.New("token"), knownvalue.Null()),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_agent.a", "rotate_trigger", "r1"),
					resource.TestCheckNoResourceAttr("codigate_agent.a", "token"),
					noRotation,
				),
			},
			{
				Config: cfg("r2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_agent.a", "token", matches(`^cga_`)),
					func(*terraform.State) error {
						if n := rotations(api); n != 1 {
							return fmt.Errorf("rotated %d times, want 1", n)
						}
						return nil
					},
				),
			},
		},
	})
}

const idpAndToken = `
resource "codigate_identity_provider" "okta" {
  name          = "Okta"
  issuer        = "https://acme.okta.com/"
  client_id     = "0oa123"
  client_secret = "s3cret"
}

resource "codigate_service_token" "ci" {
  name = "CI"
}

resource "codigate_agent" "a" { name = "home-lab" }
`

func TestAppResource(t *testing.T) {
	api, p := fake(t)
	// Bypass paths and public access need a verified domain covering the hostname.
	api.Preverified = []string{"acme.codigate.app"}
	var last *terraform.State
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				// A new app without access settings is protected but not live yet.
				Config: p + idpAndToken + `
resource "codigate_app" "wiki" {
  name     = "Wiki"
  hostname = "wiki.acme.codigate.app"
  agent_id = codigate_agent.a.id
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_app.wiki", "id", matches(`^app_`)),
					resource.TestCheckResourceAttr("codigate_app.wiki", "hostname", "wiki.acme.codigate.app"),
					resource.TestCheckResourceAttr("codigate_app.wiki", "access", "protected"),
					resource.TestCheckResourceAttr("codigate_app.wiki", "enabled", "true"),
					resource.TestCheckNoResourceAttr("codigate_app.wiki", "identity_provider_id"),
				),
			},
			{
				Config: p + idpAndToken + `
resource "codigate_app" "wiki" {
  name                 = "Wiki"
  hostname             = "wiki.acme.codigate.app"
  agent_id             = codigate_agent.a.id
  identity_provider_id = codigate_identity_provider.okta.id
  policy = {
    email_domains = ["acme.com"]
    groups        = ["engineering"]
  }
  service_token_ids = [codigate_service_token.ci.id]
  bypass_paths      = ["/health"]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("codigate_app.wiki", "identity_provider_id", "codigate_identity_provider.okta", "id"),
					resource.TestCheckResourceAttr("codigate_app.wiki", "policy.everyone", "false"),
					resource.TestCheckTypeSetElemAttr("codigate_app.wiki", "policy.email_domains.*", "acme.com"),
					resource.TestCheckTypeSetElemAttr("codigate_app.wiki", "policy.groups.*", "engineering"),
					resource.TestCheckTypeSetElemAttrPair("codigate_app.wiki", "service_token_ids.*", "codigate_service_token.ci", "id"),
					resource.TestCheckTypeSetElemAttr("codigate_app.wiki", "bypass_paths.*", "/health"),
					stateCapture(&last),
				),
			},
			{
				// The data source finds the app by hostname and agrees with the resource.
				Config: p + idpAndToken + `
resource "codigate_app" "wiki" {
  name                 = "Wiki"
  hostname             = "wiki.acme.codigate.app"
  agent_id             = codigate_agent.a.id
  enabled              = false
  identity_provider_id = codigate_identity_provider.okta.id
  policy = {
    email_domains = ["acme.com"]
    groups        = ["engineering"]
  }
  service_token_ids = [codigate_service_token.ci.id]
  bypass_paths      = ["/health"]
}

data "codigate_app" "wiki" {
  hostname   = codigate_app.wiki.hostname
  depends_on = [codigate_app.wiki]
}

data "codigate_agent" "byname" {
  name       = codigate_agent.a.name
  depends_on = [codigate_agent.a]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_app.wiki", "enabled", "false"),
					resource.TestCheckResourceAttrPair("data.codigate_app.wiki", "id", "codigate_app.wiki", "id"),
					resource.TestCheckResourceAttr("data.codigate_app.wiki", "enabled", "false"),
					resource.TestCheckResourceAttr("data.codigate_app.wiki", "live", "false"),
					resource.TestCheckResourceAttr("data.codigate_app.wiki", "access", "protected"),
					resource.TestCheckResourceAttr("data.codigate_app.wiki", "url", "https://wiki.acme.codigate.app"),
					resource.TestCheckResourceAttrPair("data.codigate_agent.byname", "id", "codigate_agent.a", "id"),
					resource.TestCheckResourceAttr("data.codigate_agent.byname", "connected", "false"),
				),
			},
			{
				ResourceName:      "codigate_app.wiki",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "codigate_app.wiki",
				ImportState:       true,
				ImportStateId:     "wiki.acme.codigate.app",
				ImportStateVerify: true,
			},
			{
				// Public access drops the sign-in settings.
				Config: p + idpAndToken + `
resource "codigate_app" "wiki" {
  name     = "Wiki"
  hostname = "wiki.acme.codigate.app"
  agent_id = codigate_agent.a.id
  access   = "public"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_app.wiki", "access", "public"),
					resource.TestCheckResourceAttr("codigate_app.wiki", "enabled", "true"),
					resource.TestCheckNoResourceAttr("codigate_app.wiki", "identity_provider_id"),
					resource.TestCheckNoResourceAttr("codigate_app.wiki", "policy"),
				),
			},
			{
				PreConfig: removeOutside(api, "codigate_app.wiki", &last),
				Config: p + idpAndToken + `
resource "codigate_app" "wiki" {
  name     = "Wiki"
  hostname = "wiki.acme.codigate.app"
  agent_id = codigate_agent.a.id
  access   = "public"
}`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAppWithoutLoginNeedsVerifiedDomain(t *testing.T) {
	_, p := fake(t)
	app := func(extra string) string {
		return p + `
resource "codigate_agent" "a" { name = "a" }
resource "codigate_app" "x" {
  name     = "X"
  hostname = "x.acme.codigate.app"
  agent_id = codigate_agent.a.id
` + extra + `
}`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: app(`access = "public"`), ExpectError: matches(`verified domain that covers x\.acme\.codigate\.app`)},
			{
				Config: p + `
resource "codigate_domain" "d" {
  name   = "acme.verified.test"
  verify = true
}
resource "codigate_agent" "a" { name = "a" }
resource "codigate_app" "x" {
  name     = "X"
  hostname = "x.acme.verified.test"
  agent_id = codigate_agent.a.id
  access   = "public"
  depends_on = [codigate_domain.d]
}`,
				Check: resource.TestCheckResourceAttr("codigate_app.x", "access", "public"),
			},
		},
	})
}

func TestAppRejectsPolicyWithoutProvider(t *testing.T) {
	_, p := fake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: p + `
resource "codigate_agent" "a" { name = "a" }
resource "codigate_app" "x" {
  name     = "X"
  hostname = "x.acme.codigate.app"
  agent_id = codigate_agent.a.id
  access   = "public"
  policy   = { everyone = true }
}`,
			ExpectError: matches(`(?i)public`),
		}},
	})
}

func TestEdgeRulesResource(t *testing.T) {
	api, p := fake(t)
	base := p + `
resource "codigate_agent" "a" { name = "a" }
resource "codigate_app" "x" {
  name     = "X"
  hostname = "x.acme.codigate.app"
  agent_id = codigate_agent.a.id
}
`
	full := base + `
resource "codigate_app_edge_rules" "x" {
  app_id          = codigate_app.x.id
  allow_ips       = ["10.0.0.0/8"]
  deny_ips        = ["203.0.113.7"]
  allow_countries = ["NL", "BE"]
  path_rules = [
    { path = "/admin", action = "login" },
    { path = "/.git", action = "block" },
  ]
  rate_limit_per_minute = 600
  ban = {
    after_rate_limits = 3
    window_seconds    = 600
    duration_seconds  = 3600
  }
  challenge = {
    mode            = "auto"
    auto_per_minute = 120
  }
  waf = {
    mode       = "block"
    exclusions = [942100]
  }
}`
	var last *terraform.State
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: full,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("codigate_app_edge_rules.x", "id", "codigate_app.x", "id"),
					resource.TestCheckTypeSetElemAttr("codigate_app_edge_rules.x", "allow_countries.*", "NL"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "path_rules.#", "2"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "path_rules.1.path", "/.git"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "rate_limit_per_minute", "600"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "ban.after_failed_logins", "0"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "challenge.difficulty", "medium"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "challenge.cookie_minutes", "30"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "waf.mode", "block"),
					resource.TestCheckTypeSetElemAttr("codigate_app_edge_rules.x", "waf.exclusions.*", "942100"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "advanced_allowed", "true"),
					stateCapture(&last),
				),
			},
			{
				ResourceName:      "codigate_app_edge_rules.x",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "codigate_app_edge_rules.x",
				ImportState:       true,
				ImportStateId:     "x.acme.codigate.app",
				ImportStateVerify: true,
			},
			{
				// Rules cleared in the dashboard show up as a diff.
				PreConfig:          func() { api.ClearEdgeRules(idOf(last, "codigate_app.x")) },
				Config:             full,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: full,
			},
			{
				// Trimming the config down to one rule clears the rest.
				Config: base + `
resource "codigate_app_edge_rules" "x" {
  app_id   = codigate_app.x.id
  deny_ips = ["203.0.113.7"]
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("codigate_app_edge_rules.x", "allow_ips"),
					resource.TestCheckNoResourceAttr("codigate_app_edge_rules.x", "path_rules"),
					resource.TestCheckNoResourceAttr("codigate_app_edge_rules.x", "rate_limit_per_minute"),
					resource.TestCheckNoResourceAttr("codigate_app_edge_rules.x", "waf"),
					resource.TestCheckResourceAttr("codigate_app_edge_rules.x", "deny_ips.#", "1"),
				),
			},
			{
				// The app is deleted outside Terraform: both drop out of state.
				PreConfig: removeOutside(api, "codigate_app.x", &last),
				Config: base + `
resource "codigate_app_edge_rules" "x" {
  app_id   = codigate_app.x.id
  deny_ips = ["203.0.113.7"]
}`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestIdentityProviderResource(t *testing.T) {
	api, p := fake(t)
	var last *terraform.State
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "codigate_identity_provider" "okta" {
  name          = "Okta"
  issuer        = "https://acme.okta.com"
  client_id     = "0oa123"
  client_secret = "s3cret"
  scopes        = ["groups"]
}
resource "codigate_identity_provider" "mail" {
  name = "Email code"
  type = "email"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_identity_provider.okta", "id", matches(`^idp_`)),
					resource.TestCheckResourceAttr("codigate_identity_provider.okta", "type", "oidc"),
					resource.TestCheckResourceAttr("codigate_identity_provider.okta", "has_client_secret", "true"),
					resource.TestCheckResourceAttr("codigate_identity_provider.okta", "groups_claim", "groups"),
					resource.TestMatchResourceAttr("codigate_identity_provider.okta", "redirect_uri", matches(`/oidc/callback$`)),
					resource.TestCheckResourceAttr("codigate_identity_provider.mail", "type", "email"),
					stateCapture(&last),
				),
			},
			{
				Config: p + `
resource "codigate_identity_provider" "okta" {
  name         = "Okta SSO"
  issuer       = "https://acme.okta.com"
  client_id    = "0oa456"
  scopes       = ["groups"]
  groups_claim = "roles"
}
resource "codigate_identity_provider" "mail" {
  name = "Email code"
  type = "email"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_identity_provider.okta", "name", "Okta SSO"),
					resource.TestCheckResourceAttr("codigate_identity_provider.okta", "client_id", "0oa456"),
					resource.TestCheckResourceAttr("codigate_identity_provider.okta", "groups_claim", "roles"),
					resource.TestCheckResourceAttr("codigate_identity_provider.okta", "has_client_secret", "false"),
					func(s *terraform.State) error {
						if idOf(s, "codigate_identity_provider.okta") != idOf(last, "codigate_identity_provider.okta") {
							return fmt.Errorf("update replaced the identity provider")
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "codigate_identity_provider.okta",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"client_secret"},
			},
			{
				PreConfig: removeOutside(api, "codigate_identity_provider.mail", &last),
				Config: p + `
resource "codigate_identity_provider" "mail" {
  name = "Email code"
  type = "email"
}`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestIdentityProviderDataSource(t *testing.T) {
	_, p := fake(t)
	resources := `
resource "codigate_identity_provider" "okta" {
  name          = "Okta"
  issuer        = "https://acme.okta.com"
  client_id     = "0oa123"
  client_secret = "s3cret"
  scopes        = ["groups"]
}
resource "codigate_identity_provider" "mail" {
  name = "Email code"
  type = "email"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: p + resources},
			{
				Config: p + resources + `
data "codigate_identity_provider" "byname" {
  name = "Okta"
}
data "codigate_identity_provider" "byid" {
  id = codigate_identity_provider.mail.id
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.codigate_identity_provider.byname", "id", "codigate_identity_provider.okta", "id"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byname", "type", "oidc"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byname", "issuer", "https://acme.okta.com"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byname", "client_id", "0oa123"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byname", "scopes.#", "1"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byname", "scopes.0", "groups"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byname", "groups_claim", "groups"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byname", "has_client_secret", "true"),
					resource.TestMatchResourceAttr("data.codigate_identity_provider.byname", "redirect_uri", matches(`/oidc/callback$`)),
					resource.TestCheckNoResourceAttr("data.codigate_identity_provider.byname", "client_secret"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byid", "name", "Email code"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byid", "type", "email"),
					resource.TestCheckNoResourceAttr("data.codigate_identity_provider.byid", "issuer"),
					resource.TestCheckNoResourceAttr("data.codigate_identity_provider.byid", "redirect_uri"),
					resource.TestCheckResourceAttr("data.codigate_identity_provider.byid", "has_client_secret", "false"),
				),
			},
			{
				Config: p + resources + `
resource "codigate_identity_provider" "okta2" {
  name      = "Okta"
  issuer    = "https://other.okta.com"
  client_id = "0oa999"
}
data "codigate_identity_provider" "byname" {
  name       = "Okta"
  depends_on = [codigate_identity_provider.okta2]
}`,
				ExpectError: matches(`Ambiguous identity provider name`),
			},
		},
	})
}

func TestDomainResource(t *testing.T) {
	api, p := fake(t)
	var last *terraform.State
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "codigate_domain" "pending" { name = "example.com" }
resource "codigate_domain" "ok" {
  name   = "acme.verified.test"
  verify = true
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_domain.pending", "id", matches(`^dom_`)),
					resource.TestCheckResourceAttr("codigate_domain.pending", "name", "example.com"),
					resource.TestCheckResourceAttr("codigate_domain.pending", "verified", "false"),
					resource.TestCheckResourceAttr("codigate_domain.pending", "challenge_type", "TXT"),
					resource.TestCheckResourceAttr("codigate_domain.pending", "challenge_name", "_codigate-challenge.example.com"),
					resource.TestMatchResourceAttr("codigate_domain.pending", "challenge_value", matches(`^codigate-verify=`)),
					resource.TestCheckResourceAttr("codigate_domain.ok", "verified", "true"),
					resource.TestCheckResourceAttrSet("codigate_domain.ok", "verified_at"),
					stateCapture(&last),
				),
			},
			{
				ResourceName:            "codigate_domain.pending",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"verify"},
			},
			{
				ResourceName:            "codigate_domain.ok",
				ImportState:             true,
				ImportStateId:           "acme.verified.test",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"verify"},
			},
			{
				PreConfig: removeOutside(api, "codigate_domain.pending", &last),
				Config: p + `
resource "codigate_domain" "pending" { name = "example.com" }
resource "codigate_domain" "ok" {
  name   = "acme.verified.test"
  verify = true
}`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestDomainVerifyWarnsUntilTheRecordExists(t *testing.T) {
	_, p := fake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			// The record is not there yet: apply succeeds with a warning, verified stays false.
			Config: p + `
resource "codigate_domain" "d" {
  name   = "example.org"
  verify = true
}`,
			Check:              resource.TestCheckResourceAttr("codigate_domain.d", "verified", "false"),
			ExpectNonEmptyPlan: true,
		}},
	})
}

func TestServiceTokenResource(t *testing.T) {
	api, p := fake(t)
	var last *terraform.State
	cfg := p + `
resource "codigate_service_token" "ci" {
  name            = "CI"
  expires_in_days = 90
}
resource "codigate_service_token" "mtls" {
  name = "Scanner"
  type = "certificate"
}`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_service_token.ci", "id", matches(`^svc_`)),
					resource.TestMatchResourceAttr("codigate_service_token.ci", "secret", matches(`^cgs_`)),
					resource.TestCheckResourceAttrSet("codigate_service_token.ci", "expires_at"),
					resource.TestCheckResourceAttr("codigate_service_token.mtls", "type", "certificate"),
					resource.TestMatchResourceAttr("codigate_service_token.mtls", "generated_certificate", matches(`BEGIN CERTIFICATE`)),
					resource.TestMatchResourceAttr("codigate_service_token.mtls", "private_key", matches(`BEGIN PRIVATE KEY`)),
					resource.TestCheckResourceAttrSet("codigate_service_token.mtls", "pkcs12"),
					resource.TestCheckResourceAttrSet("codigate_service_token.mtls", "cert_sha256"),
					stateCapture(&last),
				),
			},
			{
				ResourceName:            "codigate_service_token.ci",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret", "expires_in_days"},
			},
			{
				PreConfig:          removeOutside(api, "codigate_service_token.ci", &last),
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestLogDestinationResource(t *testing.T) {
	api, p := fake(t)
	var last *terraform.State
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + `
resource "codigate_log_destination" "siem" {
  type        = "https"
  name        = "SIEM"
  streams     = ["access", "audit"]
  url         = "https://siem.example.com/ingest"
  auth_header = "Authorization: Bearer abc"
}
resource "codigate_log_destination" "syslog" {
  type    = "syslog"
  name    = "Syslog"
  streams = ["audit"]
  host    = "logs.example.com"
}
resource "codigate_log_destination" "s3" {
  type       = "s3"
  name       = "Archive"
  streams    = ["access"]
  endpoint   = "https://s3.eu-central-1.amazonaws.com"
  region     = "eu-central-1"
  bucket     = "acme-logs"
  access_key = "AKIA123"
  secret_key = "shh"
  enabled    = false
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_log_destination.siem", "id", matches(`^ldst_`)),
					resource.TestMatchResourceAttr("codigate_log_destination.siem", "signing_secret", matches(`^whsec_`)),
					resource.TestCheckResourceAttr("codigate_log_destination.siem", "auth_header", "Authorization: Bearer abc"),
					resource.TestCheckResourceAttr("codigate_log_destination.syslog", "port", "6514"),
					resource.TestCheckResourceAttr("codigate_log_destination.s3", "enabled", "false"),
					resource.TestCheckResourceAttr("codigate_log_destination.s3", "secret_key", "shh"),
					stateCapture(&last),
				),
			},
			{
				// Updates keep the signing secret, can drop the auth header and set a CA.
				Config: p + `
resource "codigate_log_destination" "siem" {
  type    = "https"
  name    = "SIEM"
  streams = ["audit"]
  url     = "https://siem.example.com/v2"
  ca_pem  = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
}
resource "codigate_log_destination" "syslog" {
  type     = "syslog"
  name     = "Syslog"
  streams  = ["audit"]
  host     = "logs.example.com"
  port     = 514
  app_name = "codigate"
}
resource "codigate_log_destination" "s3" {
  type       = "s3"
  name       = "Archive"
  streams    = ["access"]
  endpoint   = "https://s3.eu-central-1.amazonaws.com"
  region     = "eu-central-1"
  bucket     = "acme-logs"
  prefix     = "codigate/"
  access_key = "AKIA123"
  secret_key = "shh"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_log_destination.siem", "url", "https://siem.example.com/v2"),
					resource.TestCheckNoResourceAttr("codigate_log_destination.siem", "auth_header"),
					resource.TestCheckResourceAttrSet("codigate_log_destination.siem", "ca_sha256"),
					func(s *terraform.State) error {
						before := last.RootModule().Resources["codigate_log_destination.siem"].Primary.Attributes["signing_secret"]
						after := s.RootModule().Resources["codigate_log_destination.siem"].Primary.Attributes["signing_secret"]
						if before != after {
							return fmt.Errorf("signing secret changed on update")
						}
						return nil
					},
					resource.TestCheckResourceAttr("codigate_log_destination.syslog", "port", "514"),
					resource.TestCheckResourceAttr("codigate_log_destination.syslog", "app_name", "codigate"),
					resource.TestCheckResourceAttr("codigate_log_destination.s3", "prefix", "codigate/"),
					resource.TestCheckResourceAttr("codigate_log_destination.s3", "enabled", "true"),
				),
			},
			{
				ResourceName:            "codigate_log_destination.syslog",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{},
			},
			{
				ResourceName:            "codigate_log_destination.siem",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"signing_secret", "ca_pem"},
			},
			{
				PreConfig: removeOutside(api, "codigate_log_destination.syslog", &last),
				Config: p + `
resource "codigate_log_destination" "syslog" {
  type     = "syslog"
  name     = "Syslog"
  streams  = ["audit"]
  host     = "logs.example.com"
  port     = 514
  app_name = "codigate"
}`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestBrandingResource(t *testing.T) {
	api, p := fake(t)
	// A 1x1 PNG.
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: p + fmt.Sprintf(`
resource "codigate_workspace_branding" "this" {
  accent = "#0f766e"
  logo   = %q
}`, png),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("codigate_workspace_branding.this", "id", "workspace"),
					resource.TestCheckResourceAttr("codigate_workspace_branding.this", "accent", "#0f766e"),
					resource.TestMatchResourceAttr("codigate_workspace_branding.this", "logo_data_url", matches(`^data:image/png;base64,`)),
				),
			},
			{
				ResourceName:      "codigate_workspace_branding.this",
				ImportState:       true,
				ImportStateId:     "workspace",
				ImportStateVerify: true,
			},
			{
				// Logo dropped from config: it is removed from the workspace.
				Config: p + `
resource "codigate_workspace_branding" "this" {
  accent = "#0f766e"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("codigate_workspace_branding.this", "logo"),
					resource.TestCheckNoResourceAttr("codigate_workspace_branding.this", "logo_data_url"),
				),
			},
			{
				// Branding reset in the dashboard: Terraform puts it back.
				PreConfig: func() { api.Remove("branding") },
				Config: p + `
resource "codigate_workspace_branding" "this" {
  accent = "#0f766e"
}`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestDataSourcesNotFound(t *testing.T) {
	_, p := fake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config:      p + `data "codigate_app" "x" { hostname = "nope.example.com" }`,
				ExpectError: matches(`(?i)no app`),
			},
			{
				Config:      p + `data "codigate_agent" "x" { name = "nope" }`,
				ExpectError: matches(`(?i)no agent`),
			},
			{
				Config:      p + `data "codigate_identity_provider" "x" { name = "nope" }`,
				ExpectError: matches(`(?i)no identity provider`),
			},
			{
				Config:      p + `data "codigate_identity_provider" "x" { id = "idp_nope" }`,
				ExpectError: matches(`(?i)not found|404`),
			},
			{
				Config:      p + `data "codigate_identity_provider" "x" {}`,
				ExpectError: matches(`Set exactly one of id and name`),
			},
		},
	})
}

// Acceptance tests run against a real workspace: TF_ACC=1 CODIGATE_API_TOKEN=... (admin) and
// optionally CODIGATE_API_URL. They create and destroy one agent and one app under the
// workspace's app domain.
func TestAccAgentAndApp(t *testing.T) {
	if os.Getenv("CODIGATE_API_TOKEN") == "" {
		t.Skip("set CODIGATE_API_TOKEN (admin) and TF_ACC=1 to run acceptance tests")
	}
	suffix := strings.ToLower(fmt.Sprintf("%x", os.Getpid()))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
data "codigate_workspace" "this" {}

resource "codigate_agent" "acc" { name = "tf-acc-%[1]s" }

resource "codigate_app" "acc" {
  name     = "tf-acc-%[1]s"
  hostname = "tf-acc-%[1]s.${data.codigate_workspace.this.app_domain}"
  agent_id = codigate_agent.acc.id
  enabled  = false
}`, suffix),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("codigate_agent.acc", "token", matches(`^cga_`)),
					resource.TestCheckResourceAttr("codigate_app.acc", "enabled", "false"),
				),
			},
			{
				ResourceName:            "codigate_agent.acc",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token", "rotate_trigger"},
			},
			{
				ResourceName:      "codigate_app.acc",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}
