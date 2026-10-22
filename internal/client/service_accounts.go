// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Service accounts (platform 1.0.397 on): automation identities, identity
// only — a name, an owner customer and roles. Nothing here issues a token; a
// service account's token is minted in the portal or exchanged from a GitLab
// CI ID token, and its CI links are read-only.

// ListServiceAccounts reads every page of GET /service-accounts.
func (a *API) ListServiceAccounts(ctx context.Context) ([]ServiceAccount, error) {
	limit := a.pageSize()
	params := &ServiceAccountsListParams{Limit: &limit}
	var all []ServiceAccount
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /service-accounts: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.ServiceAccountsListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /service-accounts", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		if next := rsp.JSON200.NextCursor; next != nil && *next != "" {
			params.Cursor = next
			continue
		}
		return all, nil
	}
}

// GetServiceAccount reads GET /service-accounts/{id}.
func (a *API) GetServiceAccount(ctx context.Context, id string) (*ServiceAccount, error) {
	rsp, err := a.raw.ServiceAccountsGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected(serviceAccountPath("GET", id), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// CreateServiceAccount sends POST /service-accounts with an Idempotency-Key.
func (a *API) CreateServiceAccount(ctx context.Context, body ServiceAccountCreate) (*ServiceAccount, error) {
	rsp, err := a.raw.ServiceAccountsCreateWithResponse(ctx,
		&ServiceAccountsCreateParams{IdempotencyKey: a.idempotencyKey()}, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON201 == nil {
		return nil, unexpected("POST /service-accounts", rsp.HTTPResponse)
	}
	return rsp.JSON201, nil
}

// UpdateServiceAccount sends PATCH /service-accounts/{id} (merge patch).
// `is_active: false` is a deactivation; `roles` replaces the whole set.
func (a *API) UpdateServiceAccount(ctx context.Context, id string, patch Patch) (*ServiceAccount, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.ServiceAccountsUpdateWithBodyWithResponse(ctx, id, MergePatchContentType, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected(serviceAccountPath("PATCH", id), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// DeactivateServiceAccount sends DELETE /service-accounts/{id}: the account is
// deactivated (its tokens stop at once) and kept, with its roles and links.
func (a *API) DeactivateServiceAccount(ctx context.Context, id string) error {
	rsp, err := a.raw.ServiceAccountsDeleteWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected(serviceAccountPath("DELETE", id), rsp.HTTPResponse)
	}
	return nil
}

func serviceAccountPath(method, id string) string {
	return method + " /service-accounts/" + url.PathEscape(id)
}
