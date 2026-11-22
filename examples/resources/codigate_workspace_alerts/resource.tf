variable "alert_webhook_url" {
  type      = string
  sensitive = true
}

variable "ntfy_topic_url" {
  type      = string
  sensitive = true
}

resource "codigate_workspace_alerts" "this" {
  webhook_url = var.alert_webhook_url # e.g. https://hooks.example.com/codigate
  ntfy_url    = var.ntfy_topic_url    # e.g. https://ntfy.sh/acme-alerts
}

# Verify the X-Codigate-Signature header on your webhook receiver with this secret.
output "alert_signing_secret" {
  value     = codigate_workspace_alerts.this.signing_secret
  sensitive = true
}
