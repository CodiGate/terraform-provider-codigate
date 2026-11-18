# One agent and one app behind it, signed in through an email code.
# CODIGATE_API_TOKEN must be an admin token of the workspace.
terraform {
  required_providers {
    codigate = {
      source = "codigate/codigate"
    }
  }
}

provider "codigate" {}

data "codigate_workspace" "this" {}

resource "codigate_agent" "home" {
  name = "home-lab"
}

resource "codigate_identity_provider" "email" {
  name = "Email code"
  type = "email"
}

resource "codigate_app" "wiki" {
  name                 = "Wiki"
  hostname             = "wiki.${data.codigate_workspace.this.app_domain}"
  agent_id             = codigate_agent.home.id
  identity_provider_id = codigate_identity_provider.email.id
  policy = {
    email_domains = ["example.com"]
  }
}

output "agent_token" {
  description = "Set as CODIGATE_TOKEN on the agent."
  value       = codigate_agent.home.token
  sensitive   = true
}

output "wiki_url" {
  value = "https://${codigate_app.wiki.hostname}"
}
