# Changelog

All notable changes to this provider. Versions follow semantic versioning. 1.0.0 is the first public release;
the 0.x releases before it were never published, and any of them could change what the one before did.

## Unreleased

Nothing yet.

## 1.0.0 (2026-10-02) — contract 1.0.187

The first public release of the OpenTofu and Terraform provider for the ATAILA Cloud Platform, published to both
registries as `ataila/ataila`.

### Stability promise

From this release on, the provider follows semantic versioning. Within 1.x no resource, data source or
attribute is removed or renamed, no attribute changes its type or meaning, and no change needs a configuration
or a state to change: such a change waits for 2.0.0 and is announced in this file first. A minor release adds
(resources, data sources, attributes, accepted values); a patch release fixes. The platform's API v1, which the
provider speaks, changes only by addition. `~> 1.0` takes every 1.x release.

### Contract

- The platform's `/api/v1` of platform release 1.0.187 (API 1.0.0), vendored as the platform exports it
  (`api/openapi-v1.json`). The provider needs platform release 1.0.187 or later (API 1.0.0 or later within
  major version 1) and refuses an older platform when it is configured, naming the minimum: every platform
  since 1.0.155 serves API 1.0.0, but before 1.0.176 the members had other names, and before 1.0.187 the
  `Idempotency-Key` parameter and the `Location` header were not declared.

### Provider objects

- 17 resources: `ataila_customer`, `ataila_tenant`, `ataila_tenant_membership`, `ataila_user`,
  `ataila_user_role_grant`, `ataila_ai_gateway_key`, `ataila_ai_serving_tier`, `ataila_project`,
  `ataila_project_provisioning`, `ataila_project_member`, `ataila_project_prod_lock`,
  `ataila_release_promotion`, `ataila_ai_model`, `ataila_ai_model_node_cache`, `ataila_licence_bundle`,
  `ataila_brand` and `ataila_brand_asset`.
- 27 data sources: `ataila_meta`, `ataila_whoami`, `ataila_customer`, `ataila_tenant`, `ataila_tenants`,
  `ataila_user`, `ataila_users`, `ataila_permission_catalog`, `ataila_ai_gateway`, `ataila_ai_serving_tiers`,
  `ataila_project`, `ataila_projects`, `ataila_project_stages`, `ataila_release_state`,
  `ataila_release_operation`, `ataila_ai_model`, `ataila_ai_models`, `ataila_ai_model_storage`,
  `ataila_ai_model_launch_catalog`, `ataila_ai_load_targets`, `ataila_ai_node`, `ataila_ai_nodes`,
  `ataila_dgx_clusters`, `ataila_licence`, `ataila_licence_socket_facts`, `ataila_brand` and
  `ataila_brand_asset`.
- Both CLIs are first-class: OpenTofu and Terraform 1.6 and later, every change tested against the oldest and
  the newest of each, with one state shared by both.

### Upgrading

- From 0.8.x: nothing changes in a configuration or a state; `plan` shows no changes. Move the version
  constraint to `~> 1.0`.
- From 0.7.x: the same, plus 0.8.0's changes (nested lists and objects became described nested attributes,
  values and types unchanged).
- From 0.6.x or older: 0.7.0 renamed every attribute that named an internal system (its section below lists each,
  old and new). The state upgrades itself on the first read; the configuration must use the new names.

### Distribution

- Signed with the organisation's RSA-4096 key, whose public half is `docs/signing-key.asc`.
- The GitHub release holds the archives, `SHA256SUMS`, its signature and the registry manifest, and as extra
  assets the air-gapped mirror bundle (`terraform-provider-ataila_1.0.0_mirror.zip`) and its `.sha256`.
- Problems and questions: support@ataila.com; documentation at https://www.ataila.eu/developers/terraform. The
  public repository's issue tracker is off.

### Added since 0.8.0

- `ataila_project_provisioning`: `running_stages`, `failed_stages`, `needs_action_stages` and `message`.
- `ataila_brand` data source: `attribution` and `first_party` (read-only on the platform).
- The customer resource and data source state that the GitLab group's live status is not read.
- The provider refuses a platform older than release 1.0.187 when it is configured (see Contract).
- Proven end to end on a real platform (release 1.0.196) under both CLIs: read, import, create, no diff,
  drift, frozen keys, the destroy switches, a project with its provisioning, a person, and the audit log.

### CI

- The release job no longer `go install`s goreleaser: that fetched hundreds of megabytes of goreleaser's own
  modules and filled a shared runner's disk in the v0.8.0 release. It downloads goreleaser's release binary
  for linux/amd64 at a pinned version, checked against a pinned SHA-256 like the CLIs
  (`scripts/ci/toolchain.sh`, `ensure_goreleaser`), into the job's `.tmp`, which is removed after the job. The
  job keeps building with the Go cache lint saves, and saves none.
- `release:dry-run` on the default branch exercises that path without a tag: the download, `goreleaser
  check`, and a snapshot build for the runner's platform.
- The Go cache no longer stays in the runners' builds slots: every job but lint removes it after its script,
  and lint, the one job that saves it, drops the build cache first (so the saved cache holds the modules and
  the toolchain). Each job's log ends with the size its slot is left at.
- Leak guard: the private key pattern also catches the armour of an OpenPGP private key (`BEGIN PGP PRIVATE
  KEY BLOCK` between the dashes); it caught the OpenSSH, RSA, EC, DSA, encrypted and plain PKCS#8 headers
  already. Its self-test checks every armour, and `scripts/ci/test-leak-guard.sh`, run by the leak-guard job,
  plants each in a scratch repository (in the tree and in history) and checks that a public key block, as
  `docs/signing-key.asc` holds, is no finding.
- `mirror:github` never pushes a 0.x tag and `publish:github` refuses any version below 1.0.0, in their rules
  and in the scripts; `scripts/ci/test-mirror-github.sh` (run by lint) checks the mirror against a fake public
  repository, and the publisher's unit tests refuse fake 0.x tags.
- `publish:github` attaches the air-gapped mirror bundle and its `.sha256` to the release, after checking the
  bundle against its sum.
- The leak guard also refuses GitHub token shapes (classic and fine-grained); their findings are never printed.
- `mirror:github` mirrors the default branch only; a release tag is mirrored by `mirror:github:tag`, which runs
  only after `release` succeeded, so that no tag reaches the public repository without its release.
- `leak-guard:strict` fails if one of the parent group's inherited CI/CD variables holds a value in this
  project's pipelines (they are shadowed by empty project-level variables).
- The history was rewritten once before publication so that every commit's author and committer, and every
  tag's tagger, is the company address; the trees, messages and dates are unchanged, and v0.4.0 to v0.8.0
  were re-created on the rewritten commits. A temporary job forced the rewritten default branch onto the
  public repository once and was removed again.
- The tag `v1.0.0` was re-created on the fixed commit `0573f1a` before any publication. Its first pipeline
  stopped in lint (the mirror script's test inherited the pipeline's own tag variable), so nothing was
  signed, mirrored or released from the first tag, which existed on the internal repository only. 1.0.0 is
  the first public release; there is no 1.0.1.

## 0.8.0 (2026-10-01) — contract 1.0.187

Pins the platform's `/api/v1` contract of platform release 1.0.187 (API 1.0.0), the last re-pin before
publication. The vendored `api/openapi-v1.json` is the platform's export, unchanged. No attribute is renamed and
no schema version changes: a state of 0.7.x plans clean.

### Changed

- `Idempotency-Key` is a declared header parameter of twelve operations (every create, release promotions, AI
  gateway key rotations, the node-cache `PUT` and `DELETE`). The generated client takes it as a parameter, and
  the provider passes a fresh key there instead of setting the header by hand; a request's own retries reuse
  it. No other request carries one.
- On a `202` (provisioning start, release promotion, node-cache `PUT` and `DELETE`) the provider polls the
  operation the declared `Location` header names, and refuses an answer whose `Location` and body name
  different operations. A replayed answer (`Idempotent-Replayed: true`) is logged.
- Every member of a nested list or object (AI nodes, DGX clusters, load targets, the launch catalogue, the
  model store, AI models and their node caches, socket facts, project repositories, namespaces, secret paths
  and warnings, stages, reported versions) is a nested attribute with a description: the provider's own where
  it had one, else the contract's text for that property (`internal/client/descriptions.gen.go`, generated by
  `scripts/gendesc`). The values and their types are unchanged. Every attribute in `docs/` is now described.
- A token can no longer grant or revoke a key of the `api-tokens` feature (`role_not_manageable_by_token`,
  403), and no token carries one (`mintable` is false for them in `ataila_permission_catalog`). The error
  text of `ataila_user_role_grant` says so.
- `Operation.partial` (tenant orders) is in the contract; the provider does not use it.

### CI

- Temporary files live under the build directory (`TMPDIR` is `$CI_PROJECT_DIR/.tmp`) and a default
  `after_script` removes them, and stops a gpg-agent the release job started, whether the job passed or not:
  the cross-CLI jobs left three directories in the runner's `/tmp` on every run.
- The state upgrade check upgrades from the newest release tag older than the version in `main.go`, and reads
  the schema versions that release wrote from its state instead of assuming 0. Once v0.7.0 existed, it was
  picked on main and failed the check, which expected a 0.6.x state.

## 0.7.0 (2026-10-01)

**Breaking, before publication.** Pins the platform's `/api/v1` contract of platform release 1.0.176
(API 1.0.0), which renamed every identifier that named an internal system. The provider follows it. Paths,
operation ids and the operations' `kind`s are unchanged.

### Renamed attributes (old → new)

| Where | Old | New |
|---|---|---|
| `ataila_user`, `ataila_user` and `ataila_users` data sources | `keycloak_linked` | `sso_linked` |
| | `kc_sync_status` | `sso_sync_status` |
| `ataila_project`, `ataila_project` data source | `enable_minio` | `enable_object_storage` |
| | `enable_redis` | `enable_cache` |
| | `prod_minio_node_count` | `prod_object_storage_node_count` |
| | `prod_minio_disks_per_vm` | `prod_object_storage_disks_per_vm` |
| | `enable_dr_minio_mirror` | `enable_dr_object_storage_mirror` |
| | `enable_synology_minio_replication` | `enable_nas_object_storage_replication` |
| | `harbor_namespace` | `image_registry_namespace` |
| | `vault_paths` | `secret_paths` |
| `ataila_ai_gateway_key` | `vault_path` | `secret_path` |
| | `vault_field` | `secret_field` |
| `ataila_ai_model`, `ataila_ai_model` and `ataila_ai_models` data sources | `synology_volume` | `nas_volume` |
| | `synology_path` | `nas_path` |
| `ataila_ai_model_storage` data source | `synology` | `nas` |
| `ataila_ai_nodes` and `ataila_ai_node` data sources | `prometheus_reachable` | `monitoring_reachable` |

Codes the platform sends changed with them: the 503 `vault_write_failed` is `secret_store_write_failed`
(still final), and the warnings `keycloak_*` and `provisioning_keycloak_failed` are `sso_*` and
`provisioning_sso_failed` (`keycloak_not_configured` is `sso_not_configured`). The model location value
`synology` and the provisioning stage keys are data, and keep their values.

### State upgrade

- `ataila_user`, `ataila_project`, `ataila_ai_gateway_key` and `ataila_ai_model` are at schema version 1. A
  state of 0.6.x (version 0) is upgraded by renaming its attributes; nothing is read or changed on the
  platform, and `plan` shows no changes. Data sources keep no state to upgrade. Configurations must use the
  new names.

### Changed

- `ataila_ai_model_node_cache` reads `dispatch_mode_effective` from `GET /meta` and fails before any request
  that writes when the platform fakes dispatch (`dryrun` or `simulate`), on create and on destroy. The check
  after a 202 stays.
- `ataila_release_promotion` warns before it books when `GET /meta` says the platform fakes dispatch, and
  books anyway (every non-live platform behaves so).
- `ataila_meta` has `dispatch_mode_effective` and `simulate_stage_seconds`.
- The provider's texts no longer name internal systems ("the secrets store", "monitoring").
- The vendored contract is the platform's export unchanged (1.0.176 no longer names a host).

### Distribution and CI

- Every release tag produces `terraform-provider-ataila_<version>_mirror.zip`: the binaries under both
  registry addresses in the filesystem mirror layout, with a README for `.tofurc` and `.terraformrc` (README,
  "Installing without internet access").
- Leak guard: the public host names `app.ataila.eu` (the partner portal), `www.ataila.eu`, `ataila.eu` and
  `ataila.com` are allowed, and nothing else under the company's domains; the self-test refuses an internal name
  on that list. R1 of the publication readiness review: accepted by the founder 2026-10-01; no history rewrite,
  the existing tags stand. With it, `--strict` passes on v0.4.0, v0.5.0 and v0.6.0, the list of tolerated
  history findings is empty, and a new `leak-guard:strict` job runs on the default branch and on every tag and
  gates everything that leaves GitLab.
- The release job checks the signing key before it builds (`scripts/ci/signing-key.sh`): RSA or DSA only, as the
  registries require, no passphrase, the fingerprint's key; `GPG_PRIVATE_KEY` may hold the armored key
  base64-encoded on one line, so that GitLab can mask it. README, "Signing key".
- New jobs `mirror:github` (pushes the default branch and `v*` tags to the public repository after both leak
  guards; no GitLab push mirror) and `publish:github` (publishes the release job's signed files as the tag's
  GitHub release). Each exists only while its token, `GITHUB_MIRROR_TOKEN` or `GITHUB_RELEASE_TOKEN`, is set.
- README: "Source and issues", "Signing key", "Public mirror", and the version constraint `~> 0.7.0` (also in
  the provider example).
- The acceptance tests are split into four domains and run in two shards per CLI version (8 jobs); lint checks
  that every acceptance test is in exactly one domain and every domain in the CI matrix. The acceptance jobs
  run one test binary compiled by `test-binary` and need neither Go nor the Go cache, and only lint saves that
  cache: restoring it side by side, and twenty jobs saving it at once, took longer than the tests and filled the
  runners' disks.
- The cross-CLI stage also runs the state upgrade check: a state written by the previous release's binary,
  built from its tag, plans clean under this release in both CLIs and both orders.

## 0.6.0 (2026-10-01)

Releases, AI models and AI Center. Pins the platform's `/api/v1` contract of platform release 1.0.167
(API 1.0.0): the releases and AI models of 1.0.164 plus the contract polish of 1.0.167.

### Resources

- `ataila_release_promotion`: requests a promotion of `app-api` or `www` of a Kubernetes project into
  `dev` (a named `version`), `uat` or `prod` (the version the environment below last reported), with one
  Idempotency-Key per create, and polls `release:<id>` until it succeeds. A failed operation (`rejected`,
  `release_failed`, `operation_timed_out`) fails the apply and taints the resource. A PROD request waits for
  a person in the portal: by default create ends with `awaiting_approval` and a warning;
  `wait_for_approval = true` waits for the decision (`timeouts { create }`, 60 minutes). On a platform that
  fakes dispatch, and when the timeout passes, create ends with the observed status and a warning, never
  tainting (the next apply would book a second deployment). All arguments are frozen; destroy forgets.
  Import by id; a data copy is refused.
- `ataila_project_prod_lock`: the PROD data lock; unlocking needs `confirm_unlock` (the short name), sent
  only then. Destroy forgets. Import by project id.
- `ataila_ai_model`: a catalogue row. `repo` is frozen; `status`, `location` and the other store-owned
  fields are read-only (setting them fails the plan). Destroy removes the row, not destroy-gated; the
  platform's refusals (`model_has_central_copy`, `model_has_node_caches`, `model_has_active_run`) are final
  errors naming the remedy. Import by id or `repo:<repo>`.
- `ataila_ai_model_node_cache`: PUT/DELETE with one Idempotency-Key, polling `model-store-run:<id>`
  (`timeouts { create, delete }`, 3 hours and 30 minutes). A copy the node already holds is adopted. Fails at
  once on a platform that fakes dispatch. `run_in_progress`, `no_central_copy`, `unknown_node`,
  `model_loaded_on_node` and 503 `loaded_state_unknown` are final. Import `<model_id>/<node>`.

### Data sources

- `ataila_release_state`, `ataila_release_operation`, `ataila_ai_model`, `ataila_ai_models`,
  `ataila_ai_model_storage`, `ataila_ai_load_targets`, and AI Center (read-only): `ataila_ai_nodes`,
  `ataila_ai_node`, `ataila_dgx_clusters`, `ataila_ai_model_launch_catalog`. The AI Center reads never
  fail on a monitoring outage; `prometheus_reachable` says whether the live fields could be read.

### Changed by the contract polish (1.0.167)

- `ataila_user`: destroy (deactivate) accepts the platform's 204 without a body; what did not go as planned
  in a deactivation is now only on the platform's audit row. `username` and `ad_username` stay create-only
  (the platform now refuses a change with 422 `immutable_field`). On a platform without SSO a new user is
  `partial` with the warning `keycloak_not_configured`.
- `ataila_ai_serving_tier` and `ataila_ai_serving_tiers`: `created_at` is new; `updated_at` is never null
  (it repeats `created_at` until the first change).
- `ataila_ai_gateway_key`: `models` is a sorted set on the platform too; every write answers
  `live = not_read`, and the provider reads the key back after create, change and rotation as before. A
  platform without a gateway is reported before any check of the body.
- `ataila_project`: a create refused because values are taken lists every conflict (short name, index,
  domain, repository), each with the project holding it.
- `ataila_project_provisioning` `stages` and `ataila_project_stages`: `last_run_id` and `last_run_at` of
  each stage's newest run.
- `ataila_licence_bundle`: a 409 `stale_epoch` that says `already_installed` adopts the installed document
  (the digest comparison stays as the cross-check).
- `ataila_brand_asset` uploads use the contract's named upload schema; the wire is unchanged.
- Every number of the contract is a double: budgets and spend are float64 end to end.
- The vendored contract differs from the platform's 1.0.167 export in one place: the description of
  `DELETE /users/{user_id}` no longer names the partner portal's host (it reads "the partner portal"), as the
  platform's own wording will from 1.0.170. The generated client's doc comment follows it.

### Leak guard

- Host names under any of the company's domains (`<host>.ataila.<tld>`) are findings, not only those of one
  domain. Findings already in pushed history are listed in `scripts/leak-guard-history.txt` by commit and
  sha256: a normal run reports them as `KNOWN`, `--strict` refuses them (run it before mirroring; the history
  must be rewritten first).

### Behaviour

- Numbers of AI models and AI Center keep float64 precision.
- A 503 `loaded_state_unknown` is final, like the other final 503 codes.
- A problem's list of objects (for example `conflicts`) is rendered one object per part.

### Tests

- The mock implements release promotions (approval, rejection, dry-run dispatch, reported versions),
  the PROD data lock, the model catalogue, node caches and store runs, and AI Center with and without
  monitoring. The cross-CLI state check now covers a promotion, a PROD lock and an AI model.

## 0.5.0 (2026-09-30)

Licence and brand. Same contract as 0.4.0 (platform release 1.0.162, API 1.0.0).

### Resources

- `ataila_licence_bundle` (singleton): installs the configured `acplic1.` bundle (`bundle`, sensitive)
  with `PUT /licence/bundle`, unless the platform already holds the same document (compared by
  `document_digest`, the sha256 of the bundle's document), which is adopted without sending it. A
  document installed outside the configuration shows as an update of `document_digest`, never a
  replacement. `stale_epoch` (an older bundle) is an error that is not retried. Destroy only forgets.
  Import with `current`.
- `ataila_brand` (singleton): replaces the v1 fields with `PUT /brand` and `If-Match` naming the version
  last read; a 412 is reported as a change outside Terraform since the last read ("run plan again") and
  never retried. The attribution line and `first_party` are not attributes. Asset references are
  validated by the platform. Destroy only forgets. Import with `current`.
- `ataila_brand_asset`: uploads a logo or favicon from `source` (a path) or `content_base64`; the
  content's sha256 is computed at plan time and a changed file replaces the resource. Bytes already
  stored as the same kind are adopted (warning); as the other kind they are refused. A 503
  `storage_unavailable` is final. Destroy only forgets (the file stays served). Import by asset id.

### Data sources

- `ataila_licence` (never the bundle; the serial is sensitive), `ataila_licence_socket_facts`,
  `ataila_brand`, `ataila_brand_asset` (by id or sha256).

### Tests

- The mock implements the licence (bundles, epochs, digests, masking of the serial), the socket census,
  the brand (If-Match, 412, no-op replace) and content-addressed assets.

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
