// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// MaxPageSize is the largest page the API serves; lists are read in pages of
// this size unless Config.PageSize asks for smaller ones.
const MaxPageSize = 200

// maxPages stops a list whose cursor never ends (a server bug) from looping
// forever: 10 000 pages of 200 is far beyond any real platform.
const maxPages = 10000

// Destroy refusals and the codes the provider branches on.
const (
	// CodeDestroyNotAllowed is the 403 of a destroy by a token minted without
	// allow_destroy.
	CodeDestroyNotAllowed = "destroy_not_allowed"
	// CodeImmutableField is the 422 of a change to a frozen key.
	CodeImmutableField = "immutable_field"
	// CodeCustomerArchived is the 409 of a change to an archived customer.
	CodeCustomerArchived = "customer_archived"
)

// CustomerFilter narrows GET /customers. Empty fields do not filter.
type CustomerFilter struct {
	ShortName   string
	GitlabGroup string
	Status      string
}

// TenantFilter narrows GET /tenants. Empty fields do not filter.
type TenantFilter struct {
	CustomerID string
	Slug       string
}

// Patch is a PATCH body: only the members present are changed, and a nil
// value is sent as JSON null, which clears a nullable field. The generated
// request types cannot say "null" (their pointers are omitempty), so PATCH
// bodies are built by hand.
type Patch map[string]any

func (a *API) pageSize() int {
	if a.listPageSize > 0 && a.listPageSize < MaxPageSize {
		return a.listPageSize
	}
	return MaxPageSize
}

// ListCustomers reads every page of GET /customers.
func (a *API) ListCustomers(ctx context.Context, f CustomerFilter) ([]Customer, error) {
	params := &CustomersListParams{}
	if f.ShortName != "" {
		params.ShortName = &f.ShortName
	}
	if f.GitlabGroup != "" {
		params.GitlabGroup = &f.GitlabGroup
	}
	if f.Status != "" {
		s := CustomersListParamsStatus(f.Status)
		params.Status = &s
	}
	limit := a.pageSize()
	params.Limit = &limit

	var all []Customer
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /customers: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.CustomersListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /customers", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}

// GetCustomer reads GET /customers/{id}.
func (a *API) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	rsp, err := a.raw.CustomersGetWithResponse(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /customers/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// CreateCustomer sends POST /customers. The answer's warnings say what did
// not go as planned on a create that still succeeded.
func (a *API) CreateCustomer(ctx context.Context, body CustomerCreate) (*Customer, error) {
	rsp, err := a.raw.CustomersCreateWithResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON201 == nil {
		return nil, unexpected("POST /customers", rsp.HTTPResponse)
	}
	return rsp.JSON201, nil
}

// UpdateCustomer sends PATCH /customers/{id}.
func (a *API) UpdateCustomer(ctx context.Context, id string, patch Patch) (*Customer, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.CustomersUpdateWithBodyWithResponse(ctx, id, "application/json", body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PATCH /customers/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// ArchiveCustomer sends DELETE /customers/{id}, which archives the customer.
func (a *API) ArchiveCustomer(ctx context.Context, id string) error {
	rsp, err := a.raw.CustomersDeleteWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /customers/"+id, rsp.HTTPResponse)
	}
	return nil
}

// ListTenants reads every page of GET /tenants.
func (a *API) ListTenants(ctx context.Context, f TenantFilter) ([]Tenant, error) {
	params := &TenantsListParams{}
	if f.CustomerID != "" {
		params.CustomerId = &f.CustomerID
	}
	if f.Slug != "" {
		params.Slug = &f.Slug
	}
	limit := a.pageSize()
	params.Limit = &limit

	var all []Tenant
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /tenants: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.TenantsListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /tenants", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}

// GetTenant reads GET /tenants/{id}.
func (a *API) GetTenant(ctx context.Context, id string) (*Tenant, error) {
	rsp, err := a.raw.TenantsGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /tenants/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// CreateTenant sends POST /tenants.
func (a *API) CreateTenant(ctx context.Context, body TenantCreate) (*Tenant, error) {
	rsp, err := a.raw.TenantsCreateWithResponse(ctx, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON201 == nil {
		return nil, unexpected("POST /tenants", rsp.HTTPResponse)
	}
	return rsp.JSON201, nil
}

// UpdateTenant sends PATCH /tenants/{id}.
func (a *API) UpdateTenant(ctx context.Context, id string, patch Patch) (*Tenant, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.TenantsUpdateWithBodyWithResponse(ctx, id, "application/json", body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PATCH /tenants/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// DeleteTenant sends DELETE /tenants/{id}; the API deletes only an empty tenant.
func (a *API) DeleteTenant(ctx context.Context, id string) error {
	rsp, err := a.raw.TenantsDeleteWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /tenants/"+id, rsp.HTTPResponse)
	}
	return nil
}

// GetMembership reads GET /tenants/{tenant_id}/memberships/{user_id}.
func (a *API) GetMembership(ctx context.Context, tenantID, userID string) (*Membership, error) {
	rsp, err := a.raw.TenantMembershipsGetWithResponse(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected(membershipPath("GET", tenantID, userID), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutMembership sends PUT /tenants/{tenant_id}/memberships/{user_id}.
// created is true when the membership did not exist before (201), false when
// an existing one was set (200).
func (a *API) PutMembership(ctx context.Context, tenantID, userID, role string) (m *Membership, created bool, err error) {
	rsp, err := a.raw.TenantMembershipsPutWithResponse(ctx, tenantID, userID,
		MembershipPut{Role: MembershipPutRole(role)})
	if err != nil {
		return nil, false, err
	}
	switch {
	case rsp.JSON201 != nil:
		return rsp.JSON201, true, nil
	case rsp.JSON200 != nil:
		return rsp.JSON200, false, nil
	}
	return nil, false, unexpected(membershipPath("PUT", tenantID, userID), rsp.HTTPResponse)
}

// DeleteMembership sends DELETE /tenants/{tenant_id}/memberships/{user_id}.
func (a *API) DeleteMembership(ctx context.Context, tenantID, userID string) error {
	rsp, err := a.raw.TenantMembershipsDeleteWithResponse(ctx, tenantID, userID)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected(membershipPath("DELETE", tenantID, userID), rsp.HTTPResponse)
	}
	return nil
}

func membershipPath(method, tenantID, userID string) string {
	return method + " /tenants/" + url.PathEscape(tenantID) + "/memberships/" + url.PathEscape(userID)
}

func patchBody(p Patch) (*bytes.Reader, error) {
	if p == nil {
		p = Patch{}
	}
	b, err := json.Marshal(map[string]any(p))
	if err != nil {
		return nil, fmt.Errorf("encoding the PATCH body: %w", err)
	}
	return bytes.NewReader(b), nil
}
