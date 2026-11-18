resource "codigate_identity_provider" "okta" {
  name          = "Okta"
  issuer        = "https://example.okta.com"
  client_id     = "0oa1b2c3d4"
  client_secret = var.okta_client_secret
  scopes        = ["groups"]
}

resource "codigate_identity_provider" "email" {
  name = "Email code"
  type = "email"
}

output "okta_redirect_uri" {
  value = codigate_identity_provider.okta.redirect_uri
}
