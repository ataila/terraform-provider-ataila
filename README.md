# terraform-provider-ataila

The OpenTofu and Terraform provider for the **ATAILA Cloud Platform**. It manages a platform through its
versioned API, `/api/v1`, with an API token minted in the platform's portal.

- Registry address: `ataila/ataila` (OpenTofu and Terraform registries)
- Plugin protocol: 6 only (terraform-plugin-framework)
- Licence: [MPL-2.0](LICENSE)
- Status: **0.x, pre-release.** The stability promise (additive changes only within API v1, semantic versioning
  of the provider) starts when the provider is first published. Until then any 0.x release may change.

## Supported CLIs

Both CLIs are first-class. Every change is tested against the oldest and the newest supported release of each.

| CLI | Minimum version | Tested in CI |
|---|---|---|
| OpenTofu | **1.6** | 1.6.0 and 1.12.6 |
| Terraform | **1.6** | 1.6.0 and 1.16.4 |

To stay usable from both, the provider uses nothing that exists in only one CLI or at different versions:
no actions, no ephemeral resources, no write-only attributes and no provider functions.

## Using it

```hcl
terraform {
  required_providers {
    ataila = {
      source  = "ataila/ataila"
      version = "~> 0.3"
    }
  }
}

provider "ataila" {
  endpoint = "https://portal.example.com"
}

data "ataila_whoami" "me" {}

resource "ataila_customer" "example" {
  short_name            = "EXAMPLE"
  long_name             = "Example Holdings Ltd"
  gitlab_group          = "example"
  primary_contact_email = "it@example.com"
  primary_contact_name  = "Example IT Desk"
}

resource "ataila_tenant" "builds" {
  customer_id = ataila_customer.example.id
  slug        = "example-builds"
  name        = "Build Farm"
}
```

```shell
export ATAILA_TOKEN="<token from the portal>"

# OpenTofu
tofu init && tofu plan

# Terraform
terraform init && terraform plan
```

`source = "ataila/ataila"` resolves to `registry.opentofu.org/ataila/ataila` under OpenTofu and to
`registry.terraform.io/ataila/ataila` under Terraform; the same release is published to both.

### Switching between OpenTofu and Terraform

One configuration works with both CLIs, and so does one state, with one step in one direction. A state
records the provider's full address, and `source = "ataila/ataila"` means
`registry.opentofu.org/ataila/ataila` to OpenTofu and `registry.terraform.io/ataila/ataila` to Terraform.

| From → to | What to run |
|---|---|
| Terraform → OpenTofu | Nothing. OpenTofu maps `registry.terraform.io/ataila/ataila` in a state to its own registry by itself, and its next apply records its own address. To record it at once: `tofu state replace-provider registry.terraform.io/ataila/ataila registry.opentofu.org/ataila/ataila` |
| OpenTofu → Terraform | **Required**, once, before anything else: `terraform state replace-provider registry.opentofu.org/ataila/ataila registry.terraform.io/ataila/ataila` |

Without that step Terraform stops with *Missing required provider: This state requires provider
registry.opentofu.org/ataila/ataila, but that provider isn't available* (Terraform 1.6 words it *Failed to
load plugin schemas … unavailable provider "registry.opentofu.org/ataila/ataila"*). After it,
`terraform plan` shows no changes. Both commands ask for confirmation; `-auto-approve` skips it. A remote backend is changed in
place, so switch once, not back and forth in parallel runs.

### Provider configuration

| Argument | Environment variable | Default | Meaning |
|---|---|---|---|
| `endpoint` | `ATAILA_ENDPOINT` | — | Base URL of the portal, for example `https://portal.example.com`. `/api/v1` is appended; a trailing `/api/v1` is accepted. HTTPS is required except for a loopback address. |
| `token` | `ATAILA_TOKEN` | — | API token: personal (`ataila_pat_…`) or service account (`ataila_sat_…`). Sensitive. Prefer the environment variable. |
| `ca_cert_file` | `ATAILA_CA_CERT` | — | PEM file of a private certificate authority, trusted in addition to the system roots. The environment variable takes a path or PEM text. |
| `ca_cert_pem` | | — | The same certificate authority as PEM text. Conflicts with `ca_cert_file`. |
| `allow_destroy` | | `false` | Allow destroying platform objects. A destroy needs this **and** a token minted with destroy allowed. |
| `request_timeout` | | `60s` | Timeout of one HTTP request; each retry gets its own. |

### What the provider does on every run

- Reads `GET /api/v1/meta` when it is configured and refuses a platform whose API major version is not 1, or
  whose API is older than the minimum this release needs.
- Sends `Authorization: Bearer <token>` and `User-Agent: terraform-provider-ataila/<version>`.
- Retries with backoff only on 429, 502, 503 and 504, honouring `Retry-After`. No other answer is retried.
- Treats a licence refusal (403 whose `code` starts with `licence_`) as final and quotes the platform's remedy.
- Turns every error, an RFC 9457 problem document, into a diagnostic with its title, detail, `code` and
  `request_id`, so an operator can find the request in the platform's logs.
- Sends a fresh `Idempotency-Key` with every create; that create's own retries reuse it, so a create never runs twice.
- A 404 on start means a wrong endpoint or a platform whose public API is switched off.

### Resources

| Name | What it manages | Destroy |
|---|---|---|
| [`ataila_customer`](docs/resources/customer.md) | A customer (company), with its primary tenant and GitLab group | Archives; gated |
| [`ataila_tenant`](docs/resources/tenant.md) | A further tenant of a customer | Deletes an empty tenant; gated |
| [`ataila_tenant_membership`](docs/resources/tenant_membership.md) | A user's role in a tenant | Removes the membership |
| [`ataila_user`](docs/resources/user.md) | A person: created without a password, provisioned by the platform | Deactivates; gated |
| [`ataila_user_role_grant`](docs/resources/user_role_grant.md) | One role held by one user (additive) | Removes the role |
| [`ataila_ai_gateway_key`](docs/resources/ai_gateway_key.md) | A virtual key of the AI gateway; value returned once, rotation by trigger | Deletes in the gateway, irreversibly; not gated |
| [`ataila_ai_serving_tier`](docs/resources/ai_serving_tier.md) | The pin and enabled flag of an existing serving tier | Forgets only |
| [`ataila_project`](docs/resources/project.md) | A project record and its settings; provisions nothing | Retires; gated |
| [`ataila_project_provisioning`](docs/resources/project_provisioning.md) | Runs a project's provisioning and waits until it is converged | Forgets only |
| [`ataila_project_member`](docs/resources/project_member.md) | A user's role in a project | Removes the membership |

**Destroy is off by default and needs two switches**: `allow_destroy = true` on the provider **and** a token
minted with destroy allowed. With the provider switch off, destroying a customer or a tenant fails at plan
time, before any request, and the error explains both switches and how to remove the object from the state
instead (`tofu state rm` / `terraform state rm`). With the provider switch on and a token without the flag,
the platform refuses and the error says so. A refusal by the platform itself (a customer that still has
projects, a tenant that is not empty or is a customer's primary) is reported with the platform's code and
what blocks the destroy.

**Frozen keys** (a customer's `customer_index`, `short_name`, `gitlab_group` and `edition`; a tenant's
`customer_id` and `slug`) are fixed at create. Changing one fails the plan; the provider never replaces a
customer or a tenant, because destroying a customer only archives it and an archived customer keeps its
keys, so the re-create could never succeed.

**Projects** are records first: `ataila_project` creates and changes the record (it stays `planned`), and
`ataila_project_provisioning` runs the platform's stage engine and waits until the project is converged (every
stage done, none stale), up to its `timeouts`. A change of the project marks the stages it affects stale
(`stale_stages`); the next plan then shows an in-place update of the provisioning, whose apply re-applies just
those stages. A project's `tenant_id`, `project_index`, `short_name`, `gitlab_repo_slug`, `primary_domain`,
`deployment_backend` and `network_only` are frozen like the keys above. Destroying a project retires it (both
switches): nothing on the substrate is removed, and its index, short name and domain stay reserved for good.
Destroying a provisioning only forgets it; the API cannot undo provisioning. The platform's own projects
(`is_self`) are read-only: the provider refuses to import or change them.

**Warnings** the platform returns on a request that succeeded are reported as warnings, never as errors.

**Timestamps** (`created_at`, `updated_at`) are RFC 3339 in UTC and compared as instants: the same time
written another way (`Z` or `+00:00`, more or fewer fractional digits) is never a difference.

**E-mail addresses** keep the configuration's spelling; the platform stores the domain in lower case, and
the provider compares the domain case-folded (and internationalised domains in either form).

**Legacy data:** a tenant that no customer owns is read by the data sources with a null `customer_id`; the
`ataila_tenant` resource refuses to import it. A membership holding the legacy role `developer` can be
imported and kept (leave `role` out, or set it to `developer`); planning to set `developer` fails the plan.

**Users** are deactivated, never deleted, and keep their e-mail address: creating the same address again is
refused; import the person and set `is_active = true`. `is_active = false` is a deactivation behind the same two
switches as destroy. `username` and `ad_username` are set at create only. `roles` is read-only on the user; grant
roles one by one with `ataila_user_role_grant` (a token grants only roles it carries itself, and never `admin`,
`founder` or `ssh-console`).

**The AI gateway** resources need a platform with a gateway: where none is configured, the platform answers
503 `gateway_not_configured`, which the provider reports at once and never retries. A key's value is returned
only by the create or rotation that produced it (with `expose_secret`), kept in the sensitive `secret`, and
never read again.

Import forms:

| Resource | Import id |
|---|---|
| `ataila_customer` | the id, or `short_name:<SHORT_NAME>` |
| `ataila_tenant` | the id, or `slug:<slug>` |
| `ataila_tenant_membership` | `<tenant_id>/<user_id>` |
| `ataila_user` | the id, `email:<address>` or `username:<name>` |
| `ataila_user_role_grant` | `<user_id>/<role>` |
| `ataila_ai_gateway_key` | the id, or `alias:<key_alias>` |
| `ataila_ai_serving_tier` | the tier's key |
| `ataila_project` | the id, or `short_name:<short_name>` |
| `ataila_project_provisioning` | the project id |
| `ataila_project_member` | `<project_id>/<user_id>` |

```shell
tofu import ataila_customer.example short_name:EXAMPLE        # OpenTofu
terraform import ataila_customer.example short_name:EXAMPLE   # Terraform
```

### Data sources

| Name | What it reads |
|---|---|
| [`ataila_meta`](docs/data-sources/meta.md) | API version, platform version, licence tier, tenancy mode, licensed modules, licence state |
| [`ataila_whoami`](docs/data-sources/whoami.md) | The calling principal, how it authenticated, its effective scopes, token details and expiry |
| [`ataila_customer`](docs/data-sources/customer.md) | One customer, by id, short name or GitLab group |
| [`ataila_tenant`](docs/data-sources/tenant.md) | One tenant, by id or slug |
| [`ataila_tenants`](docs/data-sources/tenants.md) | Every tenant, or every tenant of one customer (all pages) |
| [`ataila_user`](docs/data-sources/user.md) | One user, by id, e-mail address or username |
| [`ataila_users`](docs/data-sources/users.md) | Users filtered by address, username, kind, state, tenant, customer or role (all pages) |
| [`ataila_permission_catalog`](docs/data-sources/permission_catalog.md) | Every permission key, with whether it is grantable and mintable |
| [`ataila_ai_serving_tiers`](docs/data-sources/ai_serving_tiers.md) | The AI gateway's serving tiers and how each resolves now |
| [`ataila_ai_gateway`](docs/data-sources/ai_gateway.md) | The AI gateway's base URL and tier names |
| [`ataila_project`](docs/data-sources/project.md) | One project, by id or short name, with its settings, outputs and stale stages |
| [`ataila_projects`](docs/data-sources/projects.md) | Project summaries filtered by tenant, customer, status or short name (all pages) |
| [`ataila_project_stages`](docs/data-sources/project_stages.md) | A project's provisioning stages with dependencies and latest runs |

Further resources follow milestone by milestone; each ships with its documentation, examples and tests.

## Developing

Requirements: Go (the version in `go.mod`), and OpenTofu and/or Terraform on `PATH`.

```shell
go build ./...
go test ./...                 # unit tests; acceptance tests skip without TF_ACC
go generate ./...             # regenerates internal/client from api/openapi-v1.json, and docs/
```

### Acceptance tests

The acceptance tests run the real CLI against an in-process mock of `/api/v1` (`internal/acctest`), served over
TLS with its own certificate authority, so they need no platform and no credentials. Run them once per CLI:

```shell
# Terraform
TF_ACC=1 TF_ACC_TERRAFORM_PATH="$(command -v terraform)" go test ./internal/provider/ -run '^TestAcc' -v -timeout 30m

# OpenTofu
TF_ACC=1 TF_ACC_TERRAFORM_PATH="$(command -v tofu)" TF_ACC_PROVIDER_HOST=registry.opentofu.org \
  go test ./internal/provider/ -run '^TestAcc' -v -timeout 30m
```

### State shared by both CLIs

`TestCrossCLIState` builds the provider, installs it for each CLI through `dev_overrides` under that CLI's
own registry address (as a user's installation is), and runs one working directory and one state file
through both CLIs in both orders. It proves what [Switching between OpenTofu and
Terraform](#switching-between-opentofu-and-terraform) says: OpenTofu reads a Terraform state unaided;
Terraform refuses an OpenTofu state until `terraform state replace-provider`, and then reads it without a
diff. It needs both CLIs:

```shell
ATAILA_CROSS_CLI_TERRAFORM="$(command -v terraform)" ATAILA_CROSS_CLI_TOFU="$(command -v tofu)" \
  go test ./internal/provider/ -run '^TestCrossCLIState$' -v
```

### Trying a local build

Build the binary and point the CLI at it with a development override, in `~/.tofurc` for OpenTofu or
`~/.terraformrc` for Terraform (on Windows: `%APPDATA%\tofu.rc` or `%APPDATA%\terraform.rc`):

```hcl
provider_installation {
  dev_overrides {
    "registry.opentofu.org/ataila/ataila"   = "/path/to/terraform-provider-ataila"
    "registry.terraform.io/ataila/ataila"   = "/path/to/terraform-provider-ataila"
  }
  direct {}
}
```

### The API contract

`api/openapi-v1.json` is the pinned OpenAPI document of the platform's `/api/v1`, exported from the platform
release the provider is built against. `internal/client/client.gen.go` is generated from it with oapi-codegen;
the rest of `internal/client` is a thin hand-written wrapper (transport, retries, errors, TLS).
`TestGeneratedClientIsCurrent` fails whenever the two are out of step. To move to a newer contract: replace
the JSON, run `go generate ./...`, and commit both.

## Repository layout

```
main.go                     provider server (protocol 6)
internal/provider/          provider, one file per data source or resource, tests
internal/client/            generated client + hand-written wrapper
internal/acctest/           in-process mock of /api/v1 for acceptance tests
api/openapi-v1.json         the pinned API contract
examples/                   provider configuration and one example per object (rendered into docs/)
templates/                  documentation templates
docs/                       generated with tfplugindocs; do not edit by hand
scripts/leak-guard.sh       blocks anything internal from entering this repository
scripts/ci/toolchain.sh     fetches and verifies the CI toolchain
```

## CI and releases

The primary repository and the only build are on the company's GitLab; the public repository is a one-way,
read-only mirror and never builds. Every push runs: a toolchain probe, lint (gofmt, go vet, tidy modules,
generated files current), the leak guard, unit tests, the acceptance matrix (OpenTofu 1.6.0 and 1.12.6,
Terraform 1.6.0 and 1.16.4), the cross-CLI state check (the oldest pair and the newest pair) and
cross-platform builds.

A `vX.Y.Z` tag additionally runs goreleaser: zip archives for every platform, one `SHA256SUMS` file, the
registry manifest and a detached GPG signature of the sums. The tag must match `version` in `main.go`. The
release job stops with a clear message while the signing key variables (`GPG_PRIVATE_KEY`,
`GPG_FINGERPRINT`) are not configured.

### Leak guard

`scripts/leak-guard.sh` runs on every push, blocking. It scans every file, every file name and the whole history
of the branch (added lines, commit messages, author identities) for private IPv4 addresses, internal host
names, Vault paths, token shapes (platform API tokens, GitLab and Vault tokens, private keys) and internal code
names. Examples and docs use `portal.example.com`. Run it before every push:

```shell
bash scripts/leak-guard.sh
```

A finding in history must be removed by rewriting that history before anything is pushed or mirrored.

## Licence

Copyright (c) 2026 Macskásy Attila. Licensed under the [Mozilla Public License 2.0](LICENSE).
