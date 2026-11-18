# Both apps live under example.com, a verified codigate_domain. Bypass paths and
# public access need such a domain (and a paid plan); the workspace app domain
# does not count.
resource "codigate_app" "wiki" {
  name                 = "Wiki"
  hostname             = "wiki.example.com"
  agent_id             = codigate_agent.home.id
  identity_provider_id = codigate_identity_provider.okta.id
  policy = {
    email_domains = ["example.com"]
    groups        = ["engineering"]
  }
  service_token_ids = [codigate_service_token.ci.id]
  bypass_paths      = ["/health"]
}

resource "codigate_app" "status" {
  name     = "Status page"
  hostname = "status.example.com"
  agent_id = codigate_agent.home.id
  access   = "public"
}
