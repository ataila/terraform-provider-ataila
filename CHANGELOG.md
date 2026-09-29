# Changelog

All notable changes to this provider. Versions follow semantic versioning; until the first publication the
provider stays at 0.x and any release may change.

## 0.2.1 (unreleased)

Follows the platform's revised `/api/v1` contract (still API 1.0.0, unpublished).

### Changed

- `ataila_customer`: a customer configured `suspended` is created in one call (the create body now takes
  `status`); `gitlab_group` follows the tenant slug rule, 2-30 characters.
- Destroy refusals are reported with the platform's `blockers` for customers too (`{"projects": n}`).
- A create retried while its first attempt is still running is answered 429
  `idempotency_request_in_progress` with `Retry-After`; the provider waits and retries it like any 429.
- PATCH bodies are sent as JSON Merge Patch (`application/merge-patch+json`).
- Timestamps (`created_at`, `updated_at`) use a timestamp type compared as instants: `Z` and `+00:00`,
  or different fractional precision of the same time, are never a difference, never an inconsistent
  result. A tenant's `updated_at` equals `created_at` until its first change.
- E-mail addresses are compared with the domain case-folded and internationalised domains in canonical
  form; a `Name <address>` form or surrounding spaces are refused at plan time.
- Integer ids follow one rule (1 to 999999999, no leading zero): the customer data source and import
  refuse anything else with a clear message.
- `ataila_tenant_membership`: `role` may be left out to keep an existing membership's role. A legacy
  `developer` membership can be imported and kept; planning to set `developer`, or creating a membership
  without a role, fails the plan.
- `ataila_tenant`: importing a legacy tenant that no customer owns is refused with a message pointing at
  the data sources, which read such a tenant with a null `customer_id`.

### Documentation

- "Switching between OpenTofu and Terraform": OpenTofu reads a Terraform state unaided; Terraform needs
  `terraform state replace-provider registry.opentofu.org/ataila/ataila registry.terraform.io/ataila/ataila`
  once before it reads an OpenTofu state.

### Tests and CI

- The mock follows the revised contract (status on create, `blockers` everywhere, the 429 in-progress
  answer, RFC 3339 timestamps, `updated_at` on create, normalised e-mail, the id and slug rules,
  merge-patch bodies, legacy tenants, the `developer` role).
- `TestCrossCLIState` runs both orders with the provider installed through `dev_overrides`, as a user's
  is, and proves the switching rules above; the CI job requires both orders.
- `TestAccTimestampRepresentation` rewrites the state's timestamps to another representation of the same
  instants and requires no diff and no inconsistent result.
- CI: the cross-CLI jobs run in their own stage after the acceptance matrix, and every job builds the
  provider binary before its tests start, so the in-process provider of the acceptance tests is not
  starved of CPU (the test framework gives it two seconds to start).

## 0.2.0 (unreleased)

Customers, tenants and tenant memberships (the platform's tenancy API). Requires API 1.0.0.

### Resources

- `ataila_customer`: create, read, update, destroy (= archive). `customer_index` is allocated by the
  platform when omitted. Import by id or `short_name:<SHORT_NAME>`.
- `ataila_tenant`: create, read, update, destroy (= delete, only when the tenant is empty and not a
  customer's primary). Import by id or `slug:<slug>`.
- `ataila_tenant_membership`: create or adopt, read, change the role in place, destroy. Changing
  `tenant_id` or `user_id` replaces it. Import by `<tenant_id>/<user_id>`.

### Data sources

- `ataila_customer` (by id, short name or GitLab group), `ataila_tenant` (by id or slug), `ataila_tenants`
  (all tenants or one customer's, reading every page).

### Behaviour

- Frozen keys (customer: `customer_index`, `short_name`, `gitlab_group`, `edition`; tenant: `customer_id`,
  `slug`) fail the plan when changed. They never force a replacement.
- Destroying a customer or a tenant needs the provider's `allow_destroy` and a token minted with destroy
  allowed. With the provider switch off the destroy fails at plan time, before any request. A token
  without the flag is reported as such; the platform's 409 refusals are reported with their code and
  blockers. Memberships are not destroy-gated.
- Warnings returned by the platform become warning diagnostics.
- A configured e-mail address keeps its spelling when the platform stores its domain in lower case.
- Error diagnostics now include every extra member of the platform's problem document (for example
  `field`, `blockers`, `project_count`).

### Tests and CI

- The in-process mock implements the tenancy endpoints: cursor paging, filters, idempotent replay,
  frozen-key 422s, archive semantics, the destroy gate, the primary-tenant and non-empty 409s, and
  warnings on create.
- New CI job `cross-cli-state`: state written by one CLI is read by the other without a diff, for the
  oldest and the newest supported pair.

## 0.1.0 (unreleased)

First skeleton, proving the chain from the CLI to the platform's `/api/v1`.

### Provider

- Configuration: `endpoint`, `token`, `ca_cert_file` / `ca_cert_pem`, `allow_destroy`, `request_timeout`;
  environment variables `ATAILA_ENDPOINT`, `ATAILA_TOKEN`, `ATAILA_CA_CERT`.
- On configure, reads `GET /meta` and refuses an API whose major version is not 1 or which is older than the
  provider's minimum (1.0.0).
- Retries only 429, 502, 503 and 504 with backoff, honouring `Retry-After`.
- Licence refusals (403, code `licence_*`) are final and quote the platform's remedy.
- Problem details (RFC 9457) become diagnostics with title, detail, code and request id.
- A fresh `Idempotency-Key` on every create.
- Works with OpenTofu 1.6 and later and Terraform 1.6 and later.

### Data sources

- `ataila_meta`
- `ataila_whoami`
