# Terraform provider for Codigate

Manage a Codigate workspace as code: agents, apps and their access, edge rules (IP, country, rate, path, challenge, WAF), identity providers, custom domains, service tokens, log export and branding. The provider talks to the workspace API (`/api/v1`) with a workspace API token.

```terraform
terraform {
  required_providers {
    codigate = {
      source  = "codigate/codigate"
      version = "~> 0.1"
    }
  }
}

provider "codigate" {} # reads CODIGATE_API_TOKEN (and CODIGATE_API_URL)

data "codigate_workspace" "this" {}

resource "codigate_agent" "home" {
  name = "home-lab"
}

resource "codigate_identity_provider" "email" {
  name = "Email code"
  type = "email"
}

resource "codigate_app" "wiki" {
  name                 = "Wiki"
  hostname             = "wiki.${data.codigate_workspace.this.app_domain}"
  agent_id             = codigate_agent.home.id
  identity_provider_id = codigate_identity_provider.email.id
  policy               = { email_domains = ["example.com"] }
}
```

See [docs/](docs/) for every resource and data source (the same pages as on the Terraform Registry) and [examples/](examples/) for complete configurations.

## Authentication

Create an API token (`cgk_...`) in the dashboard. An **admin** token can plan and apply; a **viewer** token can only read (`terraform plan` against existing state, data sources). Pass it as `CODIGATE_API_TOKEN` or the `token` argument. `api_url` (`CODIGATE_API_URL`) defaults to `https://app.codigate.nl/api/v1`.

## Resources and data sources

| Resource | Import id |
| --- | --- |
| `codigate_agent` | agent id |
| `codigate_app` | app id or hostname |
| `codigate_app_edge_rules` | app id or hostname |
| `codigate_identity_provider` | identity provider id |
| `codigate_domain` | domain id or name |
| `codigate_service_token` | service token id |
| `codigate_log_destination` | log destination id |
| `codigate_workspace_branding` | `workspace` |

Data sources: `codigate_workspace`, `codigate_agent` (by id or name), `codigate_app` (by id or hostname).

Things to know:

- **Secrets are only known once.** The agent token, a service token's secret and a log destination's signing key are returned when Terraform creates (or rotates) them and are stored in state as sensitive values. After `terraform import` they are null. Rotate an agent token in place by changing `rotate_trigger`, or replace the resource with `terraform apply -replace=...`. An imported agent does not rotate on its first apply: the plan shows `rotate_trigger` being set and the apply only records it, so the running agent keeps its token.
- **Write-only inputs.** Identity provider client secrets and log destination auth headers are never returned by the API; Terraform keeps what you configured and cannot detect changes made in the dashboard.
- **Domain verification.** `codigate_domain` exposes the TXT record (`challenge_name`, `challenge_value`). With `verify = true` every apply checks the record until the domain is verified; while the record is missing the apply succeeds with a warning and the plan keeps showing that check.
- **Apps are created in two steps** (create, then access). If the access step is rejected, for example public access or bypass paths without a verified domain covering the hostname, Terraform marks the new app tainted and replaces it on the next apply.
- **Drift.** Anything deleted outside Terraform is dropped from state and planned for creation again.
- **Parallelism.** Older control planes could fail concurrent writes with HTTP 500 (a deadlock while publishing the edge snapshot) after the object was already saved. If you see that, run `terraform apply -parallelism=1` and import or delete the object the failed call created.

## Development

The module is part of the Codigate monorepo (`go.work`) and is published to [github.com/codigate/terraform-provider-codigate](https://github.com/codigate/terraform-provider-codigate) (see below). From this directory, with the monorepo's Go wrapper:

```sh
../tools/go.sh test ./...          # unit tests: Terraform against an in-process fake API
../tools/go.sh vet ./...
GO_TOOL=gofmt ../tools/go.sh -l .
../tools/go.sh generate ./...      # regenerate docs/ (tfplugindocs) after schema or example changes
```

Unit tests use `internal/fakeapi`, a fake of the workspace API that mirrors the real validation (including 403 for viewer tokens and 422 field errors). They need a `terraform` binary on `PATH`, or terraform-plugin-testing downloads one.

Acceptance tests run against a real workspace and create and destroy an agent and an app:

```sh
TF_ACC=1 CODIGATE_API_URL=http://127.0.0.1:8000/api/v1 CODIGATE_API_TOKEN=<admin token> ../tools/go.sh test -run TestAcc ./internal/provider/
```

Without `CODIGATE_API_TOKEN` they are skipped.

To try a local build with Terraform, run the fake API (`go run ./cmd/fakeapi -token dev`) or a local control plane, build the provider into a directory, and point Terraform at it with a CLI config file:

```hcl
provider_installation {
  dev_overrides {
    "registry.terraform.io/codigate/codigate" = "/path/to/bin"
  }
  direct {}
}
```

## Releasing

A tag `terraform-provider-vX.Y.Z` in the monorepo runs `.github/workflows/terraform-provider-release.yml`. It mirrors this directory to the public repository as one commit (message from `CHANGELOG.md`) and tags it `vX.Y.Z`. That tag runs `.github/workflows/release.yml` there: GoReleaser builds the zips, the registry manifest and `SHA256SUMS`, signs the checksums with GPG and creates the GitHub release that the Terraform Registry picks up.

The mirror is **dormant** until the owner has done these steps once:

1. Create the **public** repository `codigate/terraform-provider-codigate` (the registry requires the `terraform-provider-<name>` repository name), empty, default branch `main`.
2. Install the **Codigate Release** GitHub App (`codigate-release`) on it as well, with Contents and Workflows read/write. The mirror reuses `AGENT_RELEASE_APP_CLIENT_ID` and `AGENT_RELEASE_APP_KEY`.
3. Create a GPG signing key (RSA or DSA; the registry does not accept ECC keys) for releases. Add the ASCII-armoured private key and its passphrase as the secrets `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` **in the public repository**.
4. Set the repository variable `TF_PROVIDER_PUBLISH=true` in the monorepo, then push the tag `terraform-provider-v0.1.0`.
5. Sign in at [registry.terraform.io](https://registry.terraform.io) with GitHub as a member of the `codigate` organisation, add the GPG **public** key under the `codigate` namespace, and publish the provider from the repository. Later tags are picked up automatically.

## Licence

Apache 2.0, see [LICENSE](LICENSE).
