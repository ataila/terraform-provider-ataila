// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

// Package client talks to the ATAILA Cloud Platform API (/api/v1).
//
// client.gen.go is generated from api/openapi-v1.json by oapi-codegen and is
// never edited by hand. The rest of the package is a thin wrapper that owns
// the transport rules every call shares:
//
//   - authentication with a bearer API token;
//   - TLS trust, including a private certificate authority;
//   - retries with backoff on 429, 502, 503 and 504 only, honouring
//     Retry-After; nothing else is retried;
//   - a fresh Idempotency-Key on every create (POST), reused by that
//     request's own retries so a retried create cannot run twice;
//   - RFC 9457 problem details turned into *APIError, with a licence refusal
//     (403 with a code starting "licence_") treated as final.
package client

//go:generate go tool oapi-codegen -config oapi-codegen.yaml -o client.gen.go ../../api/openapi-v1.json
