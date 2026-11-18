resource "codigate_domain" "example" {
  name   = "example.com"
  verify = true
}

# Publish the challenge with any DNS provider, for example Cloudflare.
resource "cloudflare_dns_record" "codigate_challenge" {
  zone_id = var.cloudflare_zone_id
  name    = codigate_domain.example.challenge_name
  type    = codigate_domain.example.challenge_type
  content = codigate_domain.example.challenge_value
  ttl     = 300
}
