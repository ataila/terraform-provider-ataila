# Changelog

All notable changes to this provider. Versions follow semantic versioning; until the first publication the
provider stays at 0.x and any release may change.

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
