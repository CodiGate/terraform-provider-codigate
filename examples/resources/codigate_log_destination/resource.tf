resource "codigate_log_destination" "siem" {
  type        = "https"
  name        = "SIEM"
  streams     = ["access", "audit"]
  url         = "https://siem.example.com/ingest"
  auth_header = "Authorization: Bearer ${var.siem_token}"
}

resource "codigate_log_destination" "archive" {
  type       = "s3"
  name       = "Archive"
  streams    = ["access"]
  endpoint   = "https://s3.eu-central-1.amazonaws.com"
  region     = "eu-central-1"
  bucket     = "example-codigate-logs"
  access_key = var.s3_access_key
  secret_key = var.s3_secret_key
}
