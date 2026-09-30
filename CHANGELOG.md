# Changelog

All notable changes to this provider. Versions follow semantic versioning; until the first publication the
provider stays at 0.x and any release may change.

## 0.4.0 (2026-09-30)

Projects. Pins the platform's `/api/v1` contract of platform release 1.0.162 (API 1.0.0).

### Resources

- `ataila_project`: create records the project (status `planned`; nothing is provisioned);
  `project_index` is allocated when omitted. Update is a JSON Merge Patch of the settings, after which the
  provisioning's `stale_stages` is read into the state. `tenant_id`, `project_index`, `short_name`,
  `gitlab_repo_slug`, `primary_domain`, `deployment_backend` and `network_only` are frozen: a change fails
  the plan, naming the key, and never replaces. The platform's own projects (`is_self`) are refused at
  import and at plan time. A retired project refuses changes at plan time. Destroy = retire, behind both
  switches; destroying a retired project only forgets it. Outputs (`urls`, `harbor_namespace`,
  `gitlab_repositories`, `kubernetes_namespaces`, `vault_paths`) are ordinary computed attributes. Import
  by id or `short_name:<name>`.
- `ataila_project_provisioning`: create starts provisioning (with an Idempotency-Key reused by that
  request's retries), polls the operation until it ends, then reads the provisioning; done means
  `converged`. A project that is not converged plans an in-place update whose apply provisions again. A
  run already holding the project (`orchestration_in_progress`) is adopted, never duplicated; a stage
  waiting for an operator keeps the provider waiting (reported as a warning). `timeouts { create, update }`,
  60 minutes by default; past the timeout the resource is left (tainted when new) with the last stage in
  the error. `dispatch_mode = dryrun` fails at once. Destroy only forgets. Import by project id.
- `ataila_project_member`: PUT/DELETE of a project membership with `role` (default `developer`) and the
  recorded, not enforced, `gitlab_role`. Removal is not destroy-gated; a retired project refuses new
  members and role changes (`project_retired`, final). Import by `<project_id>/<user_id>`.

### Data sources

- `ataila_project` (by id or short name), `ataila_projects` (filters `tenant_id`, `customer_id`, `status`,
  `short_name`; every page), `ataila_project_stages`.

### Tests

- The mock implements projects, provisioning (one stage per poll; `live`, `simulate` and `dryrun`;
  injected failures and stages waiting for an operator), operations `provision:<n>` and project members.
- No attribute of a project in the state holds an IPv4 literal. The cross-CLI state check now includes a
  project, its provisioning and a member.

## 0.3.0 (unreleased)

Users, role grants and the AI gateway. Pins the platform's `/api/v1` contract with those paths (API 1.0.0).

### Resources

- `ataila_user`: create (no password is sent or returned; provisioning warnings are warnings), read,
  update, destroy = deactivate (destroy-gated; `is_active = false` is gated the same way at plan time).
  `username` and `ad_username` are set at create only. E-mail compared case-insensitively. Import by id,
  `email:<address>` or `username:<name>`; a service account is refused.
- `ataila_user_role_grant`: grant (201) or adopt a held role (200, with a warning), read, remove (not
  destroy-gated). The platform's token rules (`role_not_manageable_by_token`, `role_not_held_by_token`,
  `token_cannot_change_own_roles`, `role_not_grantable`, …) are clear errors, never retried. Import by
  `<user_id>/<role>`.
- `ataila_ai_gateway_key`: create, read (the gateway's live values; a lost key is a warning), update in
  place, rotate through `rotation_trigger`, delete (irreversible; not destroy-gated). The value is kept
  from the create or rotation that returned it and never read again. `organization_id`, `env`, `app` and
  `feature` are frozen. An adopted key's rotation fails the plan. Import by id or `alias:<key_alias>`.
- `ataila_ai_serving_tier`: sets the pin and the enabled flag of an existing tier; destroy only forgets.
  Import by key.

### Data sources

- `ataila_user`, `ataila_users` (every page), `ataila_permission_catalog`, `ataila_ai_serving_tiers`,
  `ataila_ai_gateway`.

### Behaviour

- A 503 `gateway_not_configured`, `gateway_unreachable` or `vault_write_failed` is final: the AI
  gateway is absent or unusable on that platform, and retrying cannot change it.
- Money (`soft_budget_usd`, `spend_usd`) keeps float64 precision end to end.
- Lists in a problem document are rendered as `a, b` in diagnostics.

### Tests

- The mock implements users, role grants, the permission catalogue, gateway keys, tiers and the gateway,
  with a switch for a platform without a gateway, and the tenant blocker for live gateway keys.

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
