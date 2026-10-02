// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Codes the project resources branch on.
const (
	CodeOrchestrationInProgress = "orchestration_in_progress"
	CodePlatformReadOnly        = "platform_project_read_only"
	CodeProjectRetired          = "project_retired"
	// The namespace quota (PATCH /projects/{id}/k8s-quota/{env}); a VM project
	// answers CodeVMProjectsUnsupported (releases.go), as the releases do.
	CodeQuotaRefused  = "quota_refused"
	CodeQuotaConflict = "quota_conflict"
)

// ProjectData is a project as the API answers it, decoded generically: the
// provider maps its many settings from one table. An update answer also
// carries "stale_stages".
type ProjectData map[string]any

// String returns a string member ("" when absent or null).
func (p ProjectData) String(key string) string {
	s, _ := p[key].(string)
	return s
}

// Bool returns a boolean member.
func (p ProjectData) Bool(key string) bool {
	b, _ := p[key].(bool)
	return b
}

// Strings returns a list-of-strings member.
func (p ProjectData) Strings(key string) []string {
	list, _ := p[key].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ProjectFilter narrows GET /projects.
type ProjectFilter struct {
	TenantID   string
	CustomerID string
	Status     string
	ShortName  string
}

func decodeProject(op string, resp *http.Response, body []byte, want int) (ProjectData, error) {
	if resp == nil || resp.StatusCode != want {
		return nil, unexpected(op, resp)
	}
	var p ProjectData
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%s: decoding the project: %w", op, err)
	}
	return p, nil
}

// ListProjects reads every page of GET /projects (summaries, no outputs).
func (a *API) ListProjects(ctx context.Context, f ProjectFilter) ([]ProjectData, error) {
	params := &ProjectsListParams{}
	if f.TenantID != "" {
		params.TenantId = &f.TenantID
	}
	if f.CustomerID != "" {
		params.CustomerId = &f.CustomerID
	}
	if f.Status != "" {
		s := ProjectsListParamsStatus(f.Status)
		params.Status = &s
	}
	if f.ShortName != "" {
		params.ShortName = &f.ShortName
	}
	limit := a.pageSize()
	params.Limit = &limit
	var all []ProjectData
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /projects: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.ProjectsListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.StatusCode() != http.StatusOK {
			return nil, unexpected("GET /projects", rsp.HTTPResponse)
		}
		var pg struct {
			Items      []ProjectData `json:"items"`
			NextCursor *string       `json:"next_cursor"`
		}
		if err := json.Unmarshal(rsp.Body, &pg); err != nil {
			return nil, fmt.Errorf("GET /projects: %w", err)
		}
		all = append(all, pg.Items...)
		if pg.NextCursor != nil && *pg.NextCursor != "" {
			params.Cursor = pg.NextCursor
			continue
		}
		return all, nil
	}
}

// GetProject reads GET /projects/{id}.
func (a *API) GetProject(ctx context.Context, id string) (ProjectData, error) {
	rsp, err := a.raw.ProjectsGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	return decodeProject("GET /projects/"+id, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// CreateProject sends POST /projects: a record only, nothing is provisioned.
func (a *API) CreateProject(ctx context.Context, body map[string]any) (ProjectData, error) {
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.ProjectsCreateWithBodyWithResponse(ctx,
		&ProjectsCreateParams{IdempotencyKey: a.idempotencyKey()}, "application/json", r)
	if err != nil {
		return nil, err
	}
	return decodeProject("POST /projects", rsp.HTTPResponse, rsp.Body, http.StatusCreated)
}

// UpdateProject sends PATCH /projects/{id}; the answer carries stale_stages.
func (a *API) UpdateProject(ctx context.Context, id string, patch Patch) (ProjectData, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.ProjectsUpdateWithBodyWithResponse(ctx, id, MergePatchContentType, body)
	if err != nil {
		return nil, err
	}
	return decodeProject("PATCH /projects/"+id, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// UpdateProjectQuota sends PATCH /projects/{id}/k8s-quota/{env}: one
// environment's Kubernetes namespace quota (Kubernetes projects only). The
// answer is that environment's quota after the change, with "stale_stages",
// "operation_id" (the provisioning walk the platform started, when any) and
// "dispatch_status"; decoded generically like a project.
func (a *API) UpdateProjectQuota(ctx context.Context, id, env string, patch Patch) (ProjectData, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.ProjectK8sQuotaUpdateWithBodyWithResponse(ctx, id, ProjectK8sQuotaUpdateParamsEnv(env),
		MergePatchContentType, body)
	if err != nil {
		return nil, err
	}
	return decodeProject("PATCH /projects/"+id+"/k8s-quota/"+env, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// RetireProject sends DELETE /projects/{id}, which retires the project.
func (a *API) RetireProject(ctx context.Context, id string) error {
	rsp, err := a.raw.ProjectsDeleteWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /projects/"+id, rsp.HTTPResponse)
	}
	return nil
}

// GetProvisioning reads GET /projects/{id}/provisioning.
func (a *API) GetProvisioning(ctx context.Context, id string) (*Provisioning, error) {
	rsp, err := a.raw.ProjectProvisioningGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /projects/"+id+"/provisioning", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// StartProvisioning sends POST /projects/{id}/provisioning. When an
// orchestration already holds the project, running is the operation id it
// names (409 orchestration_in_progress) and err is nil: the caller adopts it.
func (a *API) StartProvisioning(ctx context.Context, id string) (acc *Accepted, running string, err error) {
	rsp, err := a.raw.ProjectProvisioningStartWithResponse(ctx, id,
		&ProjectProvisioningStartParams{IdempotencyKey: a.idempotencyKey()})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict &&
			apiErr.Code() == CodeOrchestrationInProgress {
			if v, ok := apiErr.Extra("operation_id"); ok {
				if s, ok := v.(string); ok && s != "" {
					return nil, s, nil
				}
			}
		}
		return nil, "", err
	}
	acc, err = accepted("POST /projects/"+id+"/provisioning", rsp.HTTPResponse, rsp.JSON202)
	return acc, "", err
}

// GetStages reads GET /projects/{id}/stages.
func (a *API) GetStages(ctx context.Context, id string) (*StageGrid, error) {
	rsp, err := a.raw.ProjectStagesListWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /projects/"+id+"/stages", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

func memberPath(method, projectID, userID string) string {
	return method + " /projects/" + url.PathEscape(projectID) + "/members/" + url.PathEscape(userID)
}

// GetProjectMember reads GET /projects/{id}/members/{user_id}.
func (a *API) GetProjectMember(ctx context.Context, projectID, userID string) (*ProjectMember, error) {
	rsp, err := a.raw.ProjectMembersGetWithResponse(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected(memberPath("GET", projectID, userID), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutProjectMember sends PUT /projects/{id}/members/{user_id}. A nil
// gitlabRole is sent as null.
func (a *API) PutProjectMember(ctx context.Context, projectID, userID, role string, gitlabRole *string) (m *ProjectMember, created bool, err error) {
	body := map[string]any{"role": role, "gitlab_role": nil}
	if gitlabRole != nil {
		body["gitlab_role"] = *gitlabRole
	}
	r, err := jsonBody(body)
	if err != nil {
		return nil, false, err
	}
	rsp, err := a.raw.ProjectMembersPutWithBodyWithResponse(ctx, projectID, userID, "application/json", r)
	if err != nil {
		return nil, false, err
	}
	switch {
	case rsp.JSON201 != nil:
		return rsp.JSON201, true, nil
	case rsp.JSON200 != nil:
		return rsp.JSON200, false, nil
	}
	return nil, false, unexpected(memberPath("PUT", projectID, userID), rsp.HTTPResponse)
}

// DeleteProjectMember sends DELETE /projects/{id}/members/{user_id}.
func (a *API) DeleteProjectMember(ctx context.Context, projectID, userID string) error {
	rsp, err := a.raw.ProjectMembersDeleteWithResponse(ctx, projectID, userID)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected(memberPath("DELETE", projectID, userID), rsp.HTTPResponse)
	}
	return nil
}
