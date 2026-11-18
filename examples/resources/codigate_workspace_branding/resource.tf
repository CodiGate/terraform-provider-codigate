resource "codigate_workspace_branding" "this" {
  accent = "#0f766e"
  logo   = filebase64("${path.module}/logo.svg")
}
