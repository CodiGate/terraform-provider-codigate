# An identity provider made in the dashboard, used by an app managed in Terraform.
data "codigate_identity_provider" "entra" {
  name = "Microsoft Entra ID"
}

output "entra_redirect_uri" {
  value = data.codigate_identity_provider.entra.redirect_uri
}
