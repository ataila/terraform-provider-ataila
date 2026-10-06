// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Tenant quotas, the order catalogue and tenant orders (platform release
// ReleaseQuotasOrders and later). Decoded by hand: quota limits keep float64
// precision, and the free-form members (an order's spec, its quota check, its
// dispatch facts, a catalogue item's form) stay raw JSON, which the provider
// hands to configurations as JSON text.

// Codes the provider branches on.
const (
	// CodeTenantNotFound is the 404 of a tenant that does not exist.
	CodeTenantNotFound = "tenant_not_found"
	// CodeOrderNotFound is the 404 of an order that does not exist.
	CodeOrderNotFound = "order_not_found"
)

// TenantQuotaRow is one limit the operator set (`quotas` of TenantQuotas).
type TenantQuotaRow struct {
	Dimension string     `json:"dimension"`
	Limit     float64    `json:"limit"`
	Policy    string     `json:"policy"`
	Note      *string    `json:"note"`
	SetBy     *string    `json:"set_by"`
	SetAt     *time.Time `json:"set_at"`
}

// TenantQuotaUsageRow is one dimension next to what the tenant holds.
type TenantQuotaUsageRow struct {
	Dimension string   `json:"dimension"`
	Limit     *float64 `json:"limit"`
	Policy    *string  `json:"policy"`
	Allocated *float64 `json:"allocated"`
	Reserved  float64  `json:"reserved"`
	Remaining *float64 `json:"remaining"`
}

// TenantQuotaSet is the answer of GET and PUT /tenants/{id}/quotas.
type TenantQuotaSet struct {
	TenantID   string                `json:"tenant_id"`
	TenantName *string               `json:"tenant_name"`
	Quotas     []TenantQuotaRow      `json:"quotas"`
	Usage      []TenantQuotaUsageRow `json:"usage"`
	Notes      []string              `json:"notes"`
	Warnings   []ApiWarning          `json:"warnings"`
}

// QuotaLimit is one entry of a PUT /tenants/{id}/quotas body.
type QuotaLimit struct {
	Dimension string  `json:"dimension"`
	Limit     float64 `json:"limit"`
	Policy    string  `json:"policy"`
	Note      *string `json:"note,omitempty"`
}

// CatalogueItemData is one enabled item of GET /catalogue.
type CatalogueItemData struct {
	Key              string          `json:"key"`
	Kind             string          `json:"kind"`
	Name             string          `json:"name"`
	Edition          string          `json:"edition"`
	RequiresApproval bool            `json:"requires_approval"`
	SpecSchema       json.RawMessage `json:"spec_schema"`
	QuotaDimensions  json.RawMessage `json:"quota_dimensions"`
	PriceHint        json.RawMessage `json:"price_hint"`
	SortOrder        int64           `json:"sort_order"`
}

// OrderEventData is one entry of an order's timeline.
type OrderEventData struct {
	ID     string          `json:"id"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor"`
	Event  string          `json:"event"`
	Detail json.RawMessage `json:"detail"`
}

// OrderData is an order as GET /tenants/{id}/orders and GET /orders/{id}
// answer it (Events only on the latter).
type OrderData struct {
	ID                string           `json:"id"`
	TenantID          string           `json:"tenant_id"`
	TenantName        *string          `json:"tenant_name"`
	CustomerID        *string          `json:"customer_id"`
	ProjectID         *string          `json:"project_id"`
	CatalogueItemKey  string           `json:"catalogue_item_key"`
	CatalogueItemKind *string          `json:"catalogue_item_kind"`
	CatalogueItemName *string          `json:"catalogue_item_name"`
	Spec              json.RawMessage  `json:"spec"`
	Status            string           `json:"status"`
	QuotaCheck        json.RawMessage  `json:"quota_check"`
	Overage           json.RawMessage  `json:"overage"`
	RequestedBy       *string          `json:"requested_by"`
	ApprovedBy        *string          `json:"approved_by"`
	ApprovedAt        *time.Time       `json:"approved_at"`
	ApprovalReason    *string          `json:"approval_reason"`
	RejectedBy        *string          `json:"rejected_by"`
	RejectedAt        *time.Time       `json:"rejected_at"`
	RejectionReason   *string          `json:"rejection_reason"`
	Dispatch          json.RawMessage  `json:"dispatch"`
	OperationID       string           `json:"operation_id"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	Events            []OrderEventData `json:"events"`
}

func decodeJSON[T any](op string, resp *http.Response, body []byte, want int) (*T, error) {
	if resp == nil || resp.StatusCode != want {
		return nil, unexpected(op, resp)
	}
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("%s: decoding the answer: %w", op, err)
	}
	return &v, nil
}

// GetTenantQuotas reads GET /tenants/{id}/quotas.
func (a *API) GetTenantQuotas(ctx context.Context, tenantID string) (*TenantQuotaSet, error) {
	rsp, err := a.raw.TenantQuotasGetWithResponse(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return decodeJSON[TenantQuotaSet]("GET /tenants/"+tenantID+"/quotas", rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// PutTenantQuotas sends PUT /tenants/{id}/quotas: the tenant's whole quota
// set is replaced by limits (a dimension left out loses its limit).
func (a *API) PutTenantQuotas(ctx context.Context, tenantID string, limits []QuotaLimit) (*TenantQuotaSet, error) {
	if limits == nil {
		limits = []QuotaLimit{}
	}
	r, err := jsonBody(map[string]any{"quotas": limits})
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.TenantQuotasPutWithBodyWithResponse(ctx, tenantID, "application/json", r)
	if err != nil {
		return nil, err
	}
	return decodeJSON[TenantQuotaSet]("PUT /tenants/"+tenantID+"/quotas", rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// ListCatalogueItems reads every page of GET /catalogue.
func (a *API) ListCatalogueItems(ctx context.Context) ([]CatalogueItemData, error) {
	params := &CatalogueItemsListParams{}
	limit := a.pageSize()
	params.Limit = &limit
	var all []CatalogueItemData
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /catalogue: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.CatalogueItemsListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		pg, err := decodeJSON[struct {
			Items      []CatalogueItemData `json:"items"`
			NextCursor *string             `json:"next_cursor"`
		}]("GET /catalogue", rsp.HTTPResponse, rsp.Body, http.StatusOK)
		if err != nil {
			return nil, err
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			return all, nil
		}
		params.Cursor = pg.NextCursor
	}
}

// ListTenantOrders reads every page of GET /tenants/{id}/orders, newest
// first; statuses, when given, narrow it to those states.
func (a *API) ListTenantOrders(ctx context.Context, tenantID string, statuses []string) ([]OrderData, error) {
	params := &TenantOrdersListParams{}
	if len(statuses) > 0 {
		s := make([]TenantOrdersListParamsStatus, len(statuses))
		for i, v := range statuses {
			s[i] = TenantOrdersListParamsStatus(v)
		}
		params.Status = &s
	}
	limit := a.pageSize()
	params.Limit = &limit
	op := "GET /tenants/" + tenantID + "/orders"
	var all []OrderData
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("%s: gave up after %d pages; the cursor never ended", op, maxPages)
		}
		rsp, err := a.raw.TenantOrdersListWithResponse(ctx, tenantID, params)
		if err != nil {
			return nil, err
		}
		pg, err := decodeJSON[struct {
			Items      []OrderData `json:"items"`
			NextCursor *string     `json:"next_cursor"`
		}](op, rsp.HTTPResponse, rsp.Body, http.StatusOK)
		if err != nil {
			return nil, err
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			return all, nil
		}
		params.Cursor = pg.NextCursor
	}
}

// GetOrder reads GET /orders/{id}, with the order's timeline.
func (a *API) GetOrder(ctx context.Context, orderID string) (*OrderData, error) {
	rsp, err := a.raw.OrdersGetWithResponse(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return decodeJSON[OrderData]("GET /orders/"+orderID, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}
