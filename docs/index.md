---
page_title: "Provider: ATAILA"
description: |-
  Manage an ATAILA Cloud Platform with OpenTofu or Terraform.
---

# ATAILA Provider

Manages an ATAILA Cloud Platform through its versioned API (`/api/v1`). Works with OpenTofu 1.6 and later and Terraform 1.6 and later.

The provider talks to the platform's versioned API, `/api/v1`, with an API token minted in the portal.
It is built for **both OpenTofu and Terraform** and tested against the oldest and newest supported
release of each on every change.

| CLI | Minimum version |
|---|---|
| OpenTofu | 1.6 |
| Terraform | 1.6 |

## Example Usage

```terraform
terraform {
  required_providers {
    ataila = {
      source  = "ataila/ataila"
      # Any 1.x: within a major version nothing is removed or renamed.
      version = "~> 1.0"
    }
  }
}

# The token is best kept out of files: export ATAILA_TOKEN instead.
provider "ataila" {
  endpoint = "https://portal.example.com"

  # Only needed when the portal's certificate comes from a private
  # certificate authority.
  ca_cert_file = "ca.pem"
}
```

With the token in the environment, the same commands work with either CLI:

```shell
export ATAILA_TOKEN="<token from the portal>"

tofu init && tofu plan          # OpenTofu
terraform init && terraform plan  # Terraform
```

For machines without internet access, every release also has an air-gapped mirror bundle: the binaries
under both registry addresses in the layout `provider_installation { filesystem_mirror }` reads, for
OpenTofu and Terraform alike. The repository README, [Installing without internet
access](https://github.com/ataila/terraform-provider-ataila#installing-without-internet-access), shows the
`.tofurc` and `.terraformrc` settings.

### OpenTofu until its registry lists the provider (temporary)

**Temporary.** The OpenTofu registry does not list `ataila/ataila` yet: the signing key is in
(`opentofu/registry` PR #5671, merged on 2026-10-02), the listing (PR #5669) is still open. Until then
`tofu init` cannot download the provider, so OpenTofu installs it from a filesystem mirror; Terraform installs
from its registry as usual. Fill the mirror from the [GitHub
release](https://github.com/ataila/terraform-provider-ataila/releases): copy the signed archive for the
platform, `terraform-provider-ataila_<version>_<os>_<arch>.zip`, unchanged into
`<mirror>/registry.opentofu.org/ataila/ataila/`, or unpack the air-gapped mirror bundle,
`terraform-provider-ataila_<version>_mirror.zip`, into `<mirror>`. Then, in `~/.tofurc` (`%APPDATA%\tofu.rc` on
Windows):

```hcl
provider_installation {
  filesystem_mirror {
    path    = "/opt/terraform/mirror"
    include = ["registry.opentofu.org/ataila/ataila"]
  }
  direct {
    exclude = ["registry.opentofu.org/ataila/ataila"]
  }
}
```

`tofu init` reports the provider as `(unauthenticated)`: a mirror carries no signature for OpenTofu to check.
Check the archive against the release's signed `SHA256SUMS` yourself, with its `.sig` and the public key
[`docs/signing-key.asc`](https://github.com/ataila/terraform-provider-ataila/blob/main/docs/signing-key.asc)
in the same directory:

```shell
gpg --import signing-key.asc
gpg --verify terraform-provider-ataila_1.1.0_SHA256SUMS.sig terraform-provider-ataila_1.1.0_SHA256SUMS
sha256sum --ignore-missing -c terraform-provider-ataila_1.1.0_SHA256SUMS
```

Expect `Good signature from "ATAILA Kft. (Budapest) <support@ataila.com>"` with the fingerprint
`9997 D23D 2222 0320 3B02  B5EE 5174 0425 A2D9 15F8`, then `OK` for the archive. Once the listing is live,
delete the `provider_installation` block and run `tofu init` again. The repository README, [OpenTofu until its
registry lists the
provider](https://github.com/ataila/terraform-provider-ataila#opentofu-until-its-registry-lists-the-provider-temporary),
has the details, the bundle's check included.

Problems and questions: support@ataila.com. Documentation: <https://www.ataila.com/developers/terraform>.

## Behaviour

- On start the provider reads `GET /api/v1/meta` and refuses a platform whose API major version is not 1,
  or whose release is older than **1.0.187**, the contract this provider release is built on: every platform
  since 1.0.155 serves API 1.0.0, but before 1.0.176 the API named its members differently, and before 1.0.187
  it did not declare the `Idempotency-Key` parameter and the `Location` header the provider relies on.
- Requests answered with 429, 502, 503 or 504 are retried with backoff, honouring `Retry-After`.
  Nothing else is retried.
- A refusal by the platform licence (HTTP 403 with a code starting `licence_`) is final; the error
  quotes the remedy the platform gives.
- Every operation that declares an `Idempotency-Key` (the creates, release promotions, key rotations,
  node-cache changes) carries a fresh one, so a retried request never runs twice.
- Destroying platform objects needs `allow_destroy = true` here **and** a token minted with destroy allowed.
  Without the first, the destroy fails at plan time, before any request; without the second, the platform
  refuses it. To stop managing an object without destroying it, remove it from the state
  (`tofu state rm` / `terraform state rm`).
- Keys that the platform fixes at create (for example a customer's `short_name` or a tenant's `slug`) are
  **frozen**: changing one fails the plan with an explanation. The provider never replaces such an object,
  because destroying a customer only archives it and an archived customer keeps its keys.
- Warnings the platform returns on a successful request (for example a GitLab group that could not be
  created yet) are reported as warnings, never as errors.
- Timestamps are compared as instants: the same time written another way is never a difference.

## Switching between OpenTofu and Terraform

One configuration works with both CLIs, and so does one state, with one step in each direction. A state
records the provider's full address, and `source = "ataila/ataila"` means
`registry.opentofu.org/ataila/ataila` to OpenTofu and `registry.terraform.io/ataila/ataila` to Terraform.
`terraform init` also tries to install the address a state names, and fails on OpenTofu's; `tofu init`
installs only its own, so the address in a Terraform state stays unavailable to OpenTofu.

| From → to | What to run, once, in this order |
|---|---|
| Terraform → OpenTofu | `tofu init`, then `tofu state replace-provider registry.terraform.io/ataila/ataila registry.opentofu.org/ataila/ataila`. Without the step `tofu plan` works (`No changes`) and `tofu output` prints the same outputs, but `tofu show` and `tofu show -json` fail until it has run or one `tofu apply` has recorded OpenTofu's address |
| OpenTofu → Terraform | **Required**: `terraform state replace-provider registry.opentofu.org/ataila/ataila registry.terraform.io/ataila/ataila`, **then** `terraform init`, then `terraform plan`. A `terraform init` run before the step fails (below): run the step, then `terraform init` again |

Without the step, measured with provider 1.1.0, OpenTofu 1.12.3 and Terraform 1.9.8 on 2026-10-03:

- `tofu show` on a state Terraform wrote (`tofu providers` lists `registry.terraform.io/ataila/ataila` under
  "Providers required by state"):

  ```text
  Error: Failed to load plugin schemas
  Error while loading schemas for plugin components: failed to instantiate
  provider "registry.terraform.io/ataila/ataila" to obtain schema: unavailable
  provider "registry.terraform.io/ataila/ataila".
  ```

- `terraform init` on a state OpenTofu wrote, while it tries to download the OpenTofu address:

  ```text
  Error: Failed to query available provider packages
  Could not retrieve the list of available versions for provider
  registry.opentofu.org/ataila/ataila: provider registry registry.opentofu.org
  does not have a provider named registry.opentofu.org/ataila/ataila
  ```

  Run the step, then `terraform init` again: a `terraform plan` straight after the step stops with
  `Inconsistent dependency lock file`. After the second `init`, `terraform plan` shows no changes and
  `terraform show` works. Once the OpenTofu registry lists the provider, the first error will likely read
  differently; the order stays the same.

```shell
# Terraform → OpenTofu
tofu init
tofu state replace-provider registry.terraform.io/ataila/ataila registry.opentofu.org/ataila/ataila
tofu plan && tofu show

# OpenTofu → Terraform: replace-provider first, then init
terraform state replace-provider registry.opentofu.org/ataila/ataila registry.terraform.io/ataila/ataila
terraform init && terraform plan
```

Both `replace-provider` commands ask for confirmation; `-auto-approve` skips it. A remote backend is changed in
place, so switch once, not back and forth in parallel runs.

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `allow_destroy` (Boolean) Allow this provider to destroy platform objects. Defaults to `false`. Destroying needs this flag **and** a token minted with destroy allowed; without both, a destroy is refused.
- `ca_cert_file` (String) Path of a PEM file holding the certificate authority that signed the platform's certificate, trusted in addition to the system roots. Conflicts with `ca_cert_pem`. May also be set with `ATAILA_CA_CERT` (a path or PEM text).
- `ca_cert_pem` (String) The same certificate authority as PEM text. Conflicts with `ca_cert_file`.
- `endpoint` (String) Base URL of the platform's portal, for example `https://portal.example.com`. The provider calls `<endpoint>/api/v1`; a trailing `/api/v1` is accepted. HTTPS is required except for a loopback address. May also be set with the `ATAILA_ENDPOINT` environment variable.
- `request_timeout` (String) Timeout for one HTTP request, as a duration such as `30s` or `2m`. Each retry gets its own. Defaults to `60s`.
- `token` (String, Sensitive) API token: a personal token (`ataila_pat_…`) or a service-account token (`ataila_sat_…`), minted in the portal. May also be set with the `ATAILA_TOKEN` environment variable, which keeps it out of configuration files.
