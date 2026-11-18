resource "codigate_service_token" "ci" {
  name            = "CI"
  expires_in_days = 90
}

resource "codigate_service_token" "scanner" {
  name = "Scanner"
  type = "certificate"
}

output "ci_secret" {
  value     = codigate_service_token.ci.secret
  sensitive = true
}
