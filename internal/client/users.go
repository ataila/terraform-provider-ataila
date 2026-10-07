// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// UserFilter narrows GET /users. Empty fields do not filter; Kind "" means
// the API's default (people only), "all" lists service accounts too.
type UserFilter struct {
	Email      string
	Username   string
	Kind       string
	IsActive   *bool
	TenantID   string
	CustomerID string
	Role       string
}

// ListUsers reads every page of GET /users.
func (a *API) ListUsers(ctx context.Context, f UserFilter) ([]User, error) {
	params := &UsersListParams{}
	if f.Email != "" {
		params.Email = &f.Email
	}
	if f.Username != "" {
		params.Username = &f.Username
	}
	if f.Kind != "" {
		k := UsersListParamsKind(f.Kind)
		params.Kind = &k
	}
	params.IsActive = f.IsActive
	if f.TenantID != "" {
		var u openapi_types.UUID
		if err := u.UnmarshalText([]byte(f.TenantID)); err != nil {
			return nil, fmt.Errorf("tenant_id %q is not a UUID", f.TenantID)
		}
		params.TenantId = &u
	}
	if f.CustomerID != "" {
		params.CustomerId = &f.CustomerID
	}
	if f.Role != "" {
		params.Role = &f.Role
	}
	limit := a.pageSize()
	params.Limit = &limit

	var all []User
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /users: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.UsersListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /users", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		if next := rsp.JSON200.NextCursor; next != nil && *next != "" {
			params.Cursor = next
			continue
		}
		return all, nil
	}
}

// GetUser reads GET /users/{id}.
func (a *API) GetUser(ctx context.Context, id string) (*User, error) {
	rsp, err := a.raw.UsersGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /users/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// CreateUser sends POST /users. No password is ever sent or returned.
func (a *API) CreateUser(ctx context.Context, body UserCreate) (*User, error) {
	rsp, err := a.raw.UsersCreateWithResponse(ctx, &UsersCreateParams{IdempotencyKey: a.idempotencyKey()}, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON201 == nil {
		return nil, unexpected("POST /users", rsp.HTTPResponse)
	}
	return rsp.JSON201, nil
}

// UpdateUser sends PATCH /users/{id}. `is_active: false` is a deactivation.
func (a *API) UpdateUser(ctx context.Context, id string, patch Patch) (*User, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.UsersUpdateWithBodyWithResponse(ctx, id, MergePatchContentType, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PATCH /users/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// DeactivateUser sends DELETE /users/{id}, which deactivates the user and
// answers 204 (what did not go as planned is on the audit row only).
func (a *API) DeactivateUser(ctx context.Context, id string) error {
	rsp, err := a.raw.UsersDeleteWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /users/"+id, rsp.HTTPResponse)
	}
	return nil
}

// GetRoleGrant reads GET /users/{id}/roles/{role}.
func (a *API) GetRoleGrant(ctx context.Context, userID, role string) (*RoleGrant, error) {
	rsp, err := a.raw.UserRolesGetWithResponse(ctx, userID, role)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected(rolePath("GET", userID, role), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// GrantRole sends PUT /users/{id}/roles/{role}. granted is true when the
// role was granted now (201), false when the user already held it (200).
func (a *API) GrantRole(ctx context.Context, userID, role string) (g *RoleGrant, granted bool, err error) {
	rsp, err := a.raw.UserRolesPutWithResponse(ctx, userID, role)
	if err != nil {
		return nil, false, err
	}
	switch {
	case rsp.JSON201 != nil:
		return rsp.JSON201, true, nil
	case rsp.JSON200 != nil:
		return rsp.JSON200, false, nil
	}
	return nil, false, unexpected(rolePath("PUT", userID, role), rsp.HTTPResponse)
}

// RevokeRole sends DELETE /users/{id}/roles/{role}.
func (a *API) RevokeRole(ctx context.Context, userID, role string) error {
	rsp, err := a.raw.UserRolesDeleteWithResponse(ctx, userID, role)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected(rolePath("DELETE", userID, role), rsp.HTTPResponse)
	}
	return nil
}

func rolePath(method, userID, role string) string {
	return method + " /users/" + url.PathEscape(userID) + "/roles/" + url.PathEscape(role)
}

// ListPermissions reads every page of GET /permissions, optionally one
// feature's keys only.
func (a *API) ListPermissions(ctx context.Context, feature string) ([]Permission, error) {
	params := &PermissionsListParams{}
	if feature != "" {
		params.Feature = &feature
	}
	limit := a.pageSize()
	params.Limit = &limit
	var all []Permission
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /permissions: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.PermissionsListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /permissions", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		if next := rsp.JSON200.NextCursor; next != nil && *next != "" {
			params.Cursor = next
			continue
		}
		return all, nil
	}
}
