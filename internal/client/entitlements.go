// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Service entitlements and grants (platform 1.0.392 on): the service
// catalogue, what each customer holds, and who is granted it.

// ListServices reads every page of GET /services.
func (a *API) ListServices(ctx context.Context, includeInactive bool) ([]Service, error) {
	limit := a.pageSize()
	params := &ServicesListParams{Limit: &limit}
	if includeInactive {
		params.IncludeInactive = &includeInactive
	}
	var all []Service
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /services: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.ServicesListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /services", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}

// ListCustomerServices reads every page of GET /customers/{id}/services.
func (a *API) ListCustomerServices(ctx context.Context, customerID string) ([]CustomerService, error) {
	limit := a.pageSize()
	params := &CustomerServicesListParams{Limit: &limit}
	what := "GET /customers/" + url.PathEscape(customerID) + "/services"
	var all []CustomerService
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("%s: gave up after %d pages; the cursor never ended", what, maxPages)
		}
		rsp, err := a.raw.CustomerServicesListWithResponse(ctx, customerID, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected(what, rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}

// GetCustomerService reads GET /customers/{id}/services/{key}.
func (a *API) GetCustomerService(ctx context.Context, customerID, serviceKey string) (*CustomerService, error) {
	rsp, err := a.raw.CustomerServicesGetWithResponse(ctx, customerID, serviceKey)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected(entitlementPath("GET", customerID, serviceKey), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutCustomerService sends PUT /customers/{id}/services/{key}. created is true
// when the customer did not hold the service before (201).
func (a *API) PutCustomerService(ctx context.Context, customerID, serviceKey string, body CustomerServicePut) (
	e *CustomerServiceWritten, created bool, err error) {
	rsp, err := a.raw.CustomerServicesPutWithResponse(ctx, customerID, serviceKey, body)
	if err != nil {
		return nil, false, err
	}
	switch {
	case rsp.JSON201 != nil:
		return rsp.JSON201, true, nil
	case rsp.JSON200 != nil:
		return rsp.JSON200, false, nil
	}
	return nil, false, unexpected(entitlementPath("PUT", customerID, serviceKey), rsp.HTTPResponse)
}

// DeleteCustomerService sends DELETE /customers/{id}/services/{key}: the
// entitlement is disabled and every grant under it revoked.
func (a *API) DeleteCustomerService(ctx context.Context, customerID, serviceKey string) error {
	rsp, err := a.raw.CustomerServicesDeleteWithResponse(ctx, customerID, serviceKey)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected(entitlementPath("DELETE", customerID, serviceKey), rsp.HTTPResponse)
	}
	return nil
}

// GetServiceGrant reads GET /users/{id}/service-grants/{customer}/{key}; a
// revoked grant is returned with RevokedAt set.
func (a *API) GetServiceGrant(ctx context.Context, userID, customerID, serviceKey string) (*ServiceGrant, error) {
	rsp, err := a.raw.UserServiceGrantsGetWithResponse(ctx, userID, customerID, serviceKey)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected(grantPath("GET", userID, customerID, serviceKey), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutServiceGrant sends PUT /users/{id}/service-grants/{customer}/{key}.
// created is true when no unrevoked grant existed before (201).
func (a *API) PutServiceGrant(ctx context.Context, userID, customerID, serviceKey string, body ServiceGrantPut) (
	g *ServiceGrantWritten, created bool, err error) {
	rsp, err := a.raw.UserServiceGrantsPutWithResponse(ctx, userID, customerID, serviceKey, body)
	if err != nil {
		return nil, false, err
	}
	switch {
	case rsp.JSON201 != nil:
		return rsp.JSON201, true, nil
	case rsp.JSON200 != nil:
		return rsp.JSON200, false, nil
	}
	return nil, false, unexpected(grantPath("PUT", userID, customerID, serviceKey), rsp.HTTPResponse)
}

// DeleteServiceGrant sends DELETE /users/{id}/service-grants/{customer}/{key}:
// the grant is revoked (a revoked one answers 204 again).
func (a *API) DeleteServiceGrant(ctx context.Context, userID, customerID, serviceKey string) error {
	rsp, err := a.raw.UserServiceGrantsDeleteWithResponse(ctx, userID, customerID, serviceKey)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected(grantPath("DELETE", userID, customerID, serviceKey), rsp.HTTPResponse)
	}
	return nil
}

func entitlementPath(method, customerID, serviceKey string) string {
	return method + " /customers/" + url.PathEscape(customerID) + "/services/" + url.PathEscape(serviceKey)
}

func grantPath(method, userID, customerID, serviceKey string) string {
	return method + " /users/" + url.PathEscape(userID) + "/service-grants/" +
		url.PathEscape(customerID) + "/" + url.PathEscape(serviceKey)
}

// CustomerIDValid reports whether s can be a customer id (a positive integer).
func CustomerIDValid(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && strconv.Itoa(n) == s
}
