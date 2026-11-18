data "codigate_workspace" "this" {}

output "app_domain" {
  value = data.codigate_workspace.this.app_domain
}
