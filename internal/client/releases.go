// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Codes the release resources branch on.
const (
	CodeVMProjectsUnsupported = "vm_projects_unsupported"
	CodeVersionNotAtSource    = "version_not_at_source"
	CodeUnlockNotConfirmed    = "unlock_not_confirmed"
)

// listPages reads every page of a cursor-paged list. get fetches one page
// for a cursor (nil for the first) and returns the answer's body.
func listPages[T any](op string, get func(cursor *string) (*http.Response, []byte, error)) ([]T, *bool, error) {
	var (
		all       []T
		cursor    *string
		reachable *bool
	)
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, nil, fmt.Errorf("%s: gave up after %d pages; the cursor never ended", op, maxPages)
		}
		resp, body, err := get(cursor)
		if err != nil {
			return nil, nil, err
		}
		if resp == nil || resp.StatusCode != http.StatusOK {
			return nil, nil, unexpected(op, resp)
		}
		var pg struct {
			Items               []T     `json:"items"`
			NextCursor          *string `json:"next_cursor"`
			PrometheusReachable *bool   `json:"prometheus_reachable"`
		}
		if err := json.Unmarshal(body, &pg); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", op, err)
		}
		all = append(all, pg.Items...)
		if pg.PrometheusReachable != nil {
			if reachable == nil {
				reachable = pg.PrometheusReachable
			} else if !*pg.PrometheusReachable {
				reachable = pg.PrometheusReachable
			}
		}
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			return all, reachable, nil
		}
		cursor = pg.NextCursor
	}
}

// RequestPromotion sends POST /projects/{id}/release-promotions and returns
// the operation (release:<id>). The request carries a fresh Idempotency-Key
// that its own retries reuse, so a retry never books a second deployment.
func (a *API) RequestPromotion(ctx context.Context, projectID string, body ReleasePromotionCreate) (*Operation, error) {
	rsp, err := a.raw.ReleasePromotionsCreateWithResponse(ctx, projectID, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON202 == nil {
		return nil, unexpected("POST /projects/"+projectID+"/release-promotions", rsp.HTTPResponse)
	}
	return rsp.JSON202, nil
}

// GetReleaseOperation reads GET /release-operations/{id}.
func (a *API) GetReleaseOperation(ctx context.Context, id string) (*ReleaseOperation, error) {
	rsp, err := a.raw.ReleaseOperationsGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /release-operations/"+id, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// GetReleaseState reads GET /projects/{id}/release-state.
func (a *API) GetReleaseState(ctx context.Context, projectID string) (*ReleaseState, error) {
	rsp, err := a.raw.ReleaseStateGetWithResponse(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /projects/"+projectID+"/release-state", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// ListReleaseOperations reads every page of a project's release history.
func (a *API) ListReleaseOperations(ctx context.Context, projectID string) ([]ReleaseOperation, error) {
	limit := a.pageSize()
	items, _, err := listPages[ReleaseOperation]("GET /projects/"+projectID+"/release-operations",
		func(cursor *string) (*http.Response, []byte, error) {
			rsp, err := a.raw.ReleaseOperationsListWithResponse(ctx, projectID,
				&ReleaseOperationsListParams{Limit: &limit, Cursor: cursor})
			if err != nil {
				return nil, nil, err
			}
			return rsp.HTTPResponse, rsp.Body, nil
		})
	return items, err
}

// GetProdLock reads GET /projects/{id}/prod-lock.
func (a *API) GetProdLock(ctx context.Context, projectID string) (*ProdLock, error) {
	rsp, err := a.raw.ProdLockGetWithResponse(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /projects/"+projectID+"/prod-lock", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutProdLock sends PUT /projects/{id}/prod-lock. confirmUnlock is sent only
// to unlock.
func (a *API) PutProdLock(ctx context.Context, projectID string, locked bool, confirmUnlock string) (*ProdLock, error) {
	body := ProdLockPut{Locked: locked}
	if !locked {
		body.ConfirmUnlock = &confirmUnlock
	}
	rsp, err := a.raw.ProdLockPutWithResponse(ctx, projectID, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PUT /projects/"+projectID+"/prod-lock", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// ReleaseOperationID is the native id of an operation id release:<n>.
func ReleaseOperationID(operationID string) (string, bool) {
	const prefix = "release:"
	if len(operationID) <= len(prefix) || operationID[:len(prefix)] != prefix {
		return "", false
	}
	n := operationID[len(prefix):]
	if _, err := strconv.ParseUint(n, 10, 63); err != nil {
		return "", false
	}
	return n, true
}
