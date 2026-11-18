# Changelog

Every release in [github.com/codigate/terraform-provider-codigate](https://github.com/codigate/terraform-provider-codigate/releases) is one commit with the section below as its message, and is published to the Terraform Registry as `codigate/codigate`.

## 0.1.1

- Dependencies: golang.org/x/crypto 0.59.0, golang.org/x/net 0.63.0 and google.golang.org/grpc 1.85.0, for the advisories govulncheck reported in 0.1.0 (GO-2026-5026, -5970, -6061, -6348, -6443, -6603, -6611, -6612, -6617). 0.1.0 was never published to the Registry.

## 0.1.0

First release.

- Resources: `codigate_agent`, `codigate_app`, `codigate_app_edge_rules`, `codigate_identity_provider`, `codigate_domain`, `codigate_service_token`, `codigate_log_destination`, `codigate_workspace_branding`, all importable.
- Data sources: `codigate_workspace`, `codigate_agent`, `codigate_app`.
- Authentication with a workspace API token (`CODIGATE_API_TOKEN`); viewer tokens can plan and read, admin tokens can apply.
