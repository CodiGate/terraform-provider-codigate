terraform {
  required_providers {
    codigate = {
      source  = "codigate/codigate"
      version = "~> 0.1"
    }
  }
}

# Reads CODIGATE_API_TOKEN (and CODIGATE_API_URL) from the environment when not set here.
provider "codigate" {
  # api_url = "https://app.codigate.nl/api/v1"
  # token   = var.codigate_token
}
