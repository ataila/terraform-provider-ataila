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

// A project's backups (platform 1.0.399): the plan, which is replaced as a
// whole or returned to the default, and its sets and exports, read only.

func backupPath(projectID, rest string) string {
	return "/projects/" + url.PathEscape(projectID) + "/" + rest
}

// GetBackupPlan reads GET /projects/{id}/backup-plan. Every project has a plan;
// a 404 *APIError means the project is gone or out of the token's reach.
func (a *API) GetBackupPlan(ctx context.Context, projectID string) (*BackupPlan, error) {
	rsp, err := a.raw.ProjectBackupPlanGetWithResponse(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET "+backupPath(projectID, "backup-plan"), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutBackupPlan replaces the plan as a whole. With version non-nil the replace
// is conditional (If-Match): a 412 *APIError carries current_version.
func (a *API) PutBackupPlan(ctx context.Context, projectID string, body BackupPlanPut, version *int) (*BackupPlan, error) {
	params := &ProjectBackupPlanPutParams{}
	if version != nil {
		etag := `"` + strconv.Itoa(*version) + `"`
		params.IfMatch = &etag
	}
	rsp, err := a.raw.ProjectBackupPlanPutWithResponse(ctx, projectID, params, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PUT "+backupPath(projectID, "backup-plan"), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// ResetBackupPlan sends DELETE /projects/{id}/backup-plan: back to the default
// plan. It never switches backups off.
func (a *API) ResetBackupPlan(ctx context.Context, projectID string) error {
	rsp, err := a.raw.ProjectBackupPlanDeleteWithResponse(ctx, projectID)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE "+backupPath(projectID, "backup-plan"), rsp.HTTPResponse)
	}
	return nil
}

// BackupSetFilter narrows GET /projects/{id}/backup-sets; empty fields do not filter.
type BackupSetFilter struct {
	Environment, Tier, Status string
}

// ListBackupSets reads a project's sets, newest first, until it holds max sets
// or the list ends. more reports that further sets matched.
func (a *API) ListBackupSets(ctx context.Context, projectID string, f BackupSetFilter, max int) (sets []BackupSet, more bool, err error) {
	params := &ProjectBackupSetsListParams{}
	if f.Environment != "" {
		e := ProjectBackupSetsListParamsEnvironment(f.Environment)
		params.Environment = &e
	}
	if f.Tier != "" {
		t := ProjectBackupSetsListParamsTier(f.Tier)
		params.Tier = &t
	}
	if f.Status != "" {
		s := ProjectBackupSetsListParamsStatus(f.Status)
		params.Status = &s
	}
	where := "GET " + backupPath(projectID, "backup-sets")
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, false, fmt.Errorf("%s: gave up after %d pages; the cursor never ended", where, maxPages)
		}
		limit := min(max-len(sets), a.pageSize())
		params.Limit = &limit
		rsp, err := a.raw.ProjectBackupSetsListWithResponse(ctx, projectID, params)
		if err != nil {
			return nil, false, err
		}
		if rsp.JSON200 == nil {
			return nil, false, unexpected(where, rsp.HTTPResponse)
		}
		sets = append(sets, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		hasNext := next != nil && *next != ""
		if len(sets) >= max {
			return sets[:max], hasNext || len(sets) > max, nil
		}
		if !hasNext {
			return sets, false, nil
		}
		params.Cursor = next
	}
}

// ListBackupExports reads every export of a project, newest first (exports
// expire within days, so the list stays short); status "" does not filter.
func (a *API) ListBackupExports(ctx context.Context, projectID, status string) ([]BackupExport, error) {
	limit := a.pageSize()
	params := &ProjectBackupExportsListParams{Limit: &limit}
	if status != "" {
		s := ProjectBackupExportsListParamsStatus(status)
		params.Status = &s
	}
	where := "GET " + backupPath(projectID, "backup-exports")
	var all []BackupExport
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("%s: gave up after %d pages; the cursor never ended", where, maxPages)
		}
		rsp, err := a.raw.ProjectBackupExportsListWithResponse(ctx, projectID, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected(where, rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}
