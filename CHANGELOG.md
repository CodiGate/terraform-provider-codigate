# Changelog

Every release in [github.com/codigate/terraform-provider-codigate](https://github.com/codigate/terraform-provider-codigate/releases) is one commit with the section below as its message, and is published to the Terraform Registry as `codigate/codigate`.

## 0.3.0

- `codigate_app_edge_rules`: `challenge.skip_paths`, path prefixes where the browser challenge is not shown (for example `/api/` or `/webhooks/`), and `waf.path_exclusions`, CRS attack rules (910000-948999) switched off under one path prefix only (P2-32 in the API). Paths are lowercase, not `/`, without `..`, `%`, `?`, `#`, `;`, `*` or `\`; at most 50 skip paths, 50 path exclusions and 20 rules per path. Leaving them out and setting them to `[]` both mean none, without drift.
- `codigate_app_edge_rules`: the API's limits are checked at plan time: `waf.exclusions` (rule ids 1-999999, at most 100), `challenge.cookie_minutes` (5-1440) and `challenge.auto_per_minute` (10-1000000).
- New resource `codigate_workspace_alerts` (`/workspace/alerts`): the webhook and ntfy channels for origin health alerts. `webhook_url` and `ntfy_url` are sensitive, checked at plan time like the API (`https://` only, at most 2000 characters, no spaces, a host) and each turned off by leaving it out. `signing_secret` (sensitive) is set when Terraform adds the webhook and kept when the URL changes; `has_secret` and `allowed` (the plan allows these channels) are read-only. Import with id `workspace`; destroy turns both channels off. Rotating the secret and sending a test alert stay API-only.
- Data source `codigate_app`: `not_live_reason`, `agent_connected` and `created_at`.

## 0.2.0

- New data source `codigate_identity_provider`: looks up an identity provider by `id` or `name` (for example one made in the dashboard) and returns its type, issuer, client id, scopes, groups claim, redirect URI and whether a client secret is stored. The client secret itself is never returned.
- Fix: `codigate_app` with `access = "public"` was rejected by the API (422 "service tokens field must be an array") because the provider sent `null` for `service_tokens` and `bypass_paths`.

## 0.1.1

- Dependencies: golang.org/x/crypto 0.59.0, golang.org/x/net 0.63.0 and google.golang.org/grpc 1.85.0, for the advisories govulncheck reported in 0.1.0 (GO-2026-5026, -5970, -6061, -6348, -6443, -6603, -6611, -6612, -6617). Do not use 0.1.0.

## 0.1.0

First release.

- Resources: `codigate_agent`, `codigate_app`, `codigate_app_edge_rules`, `codigate_identity_provider`, `codigate_domain`, `codigate_service_token`, `codigate_log_destination`, `codigate_workspace_branding`, all importable.
- Data sources: `codigate_workspace`, `codigate_agent`, `codigate_app`.
- Authentication with a workspace API token (`CODIGATE_API_TOKEN`); viewer tokens can plan and read, admin tokens can apply.
