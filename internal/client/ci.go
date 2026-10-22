// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
)

// GitLab CI workload identity (platform 1.0.395 on): a pipeline holds no
// long-lived API token. Its job asks GitLab for an ID token (`id_tokens:`) and
// the provider exchanges it at POST /auth/ci-token for a one-hour token of the
// service account an operator linked to that GitLab project in the portal.

// ExchangeCIToken exchanges the configured GitLab CI ID token for an API token
// and makes every later request use it. The exchange carries no Authorization
// header (the ID token is its proof), is never idempotency-keyed (its answer is
// a fresh secret), and is retried like any request on a 429 or 503. The
// transport calls it again when the token expires during a long apply.
func (a *API) ExchangeCIToken(ctx context.Context) (*CiToken, error) {
	if a.idToken == "" {
		return nil, errors.New("no GitLab CI ID token to exchange")
	}
	rsp, err := a.raw.CiTokenExchangeWithResponse(withoutCredential(ctx),
		CiTokenExchangeJSONRequestBody{IdToken: a.idToken})
	if err != nil {
		return nil, err
	}
	if rsp.JSON201 == nil || rsp.JSON201.Token == "" {
		return nil, unexpected("POST /auth/ci-token", rsp.HTTPResponse)
	}
	a.transport.setToken(rsp.JSON201.Token)
	return rsp.JSON201, nil
}

// UsesCIToken reports whether this API authenticates with exchanged CI tokens.
func (a *API) UsesCIToken() bool { return a.idToken != "" }
