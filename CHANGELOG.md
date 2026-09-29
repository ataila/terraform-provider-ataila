# Changelog

All notable changes to this provider. Versions follow semantic versioning; until the first publication the
provider stays at 0.x and any release may change.

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
