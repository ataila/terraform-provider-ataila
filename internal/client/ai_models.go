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
	"strings"
)

// Codes the AI model resources branch on.
const (
	CodeRepoTaken          = "repo_taken"
	CodeRunInProgress      = "run_in_progress"
	CodeLoadedStateUnknown = "loaded_state_unknown"
	CodeNoCentralCopy      = "no_central_copy"
	CodeUnknownNode        = "unknown_node"
	CodeModelLoadedOnNode  = "model_loaded_on_node"
)

// Record is an API object decoded generically: numbers keep float64
// precision (the generated models use float32) and the many optional
// members are read by name.
type Record = ProjectData

// Float returns a number member and whether it was a number.
func (p ProjectData) Float(key string) (float64, bool) {
	f, ok := p[key].(float64)
	return f, ok
}

// Map returns an object member.
func (p ProjectData) Map(key string) map[string]any {
	m, _ := p[key].(map[string]any)
	return m
}

// List returns a list member.
func (p ProjectData) List(key string) []any {
	l, _ := p[key].([]any)
	return l
}

func decodeRecord(op string, resp *http.Response, body []byte, want ...int) (Record, error) {
	ok := false
	for _, w := range want {
		ok = ok || (resp != nil && resp.StatusCode == w)
	}
	if !ok {
		return nil, unexpected(op, resp)
	}
	var r Record
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%s: decoding the answer: %w", op, err)
	}
	return r, nil
}

// ── the catalogue ────────────────────────────────────────────────────────────

// ListAIModels reads every page of GET /ai-models with the API's filters.
func (a *API) ListAIModels(ctx context.Context, repo, status, category, gatewayTier string) ([]Record, error) {
	limit := a.pageSize()
	params := &AiModelsListParams{Limit: &limit}
	for _, f := range []struct {
		v   string
		dst **string
	}{{repo, &params.Repo}, {status, &params.Status}, {category, &params.Category}, {gatewayTier, &params.GatewayTier}} {
		if f.v != "" {
			v := f.v
			*f.dst = &v
		}
	}
	items, _, err := listPages[Record]("GET /ai-models", func(cursor *string) (*http.Response, []byte, error) {
		params.Cursor = cursor
		rsp, err := a.raw.AiModelsListWithResponse(ctx, params)
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
	return items, err
}

// GetAIModel reads GET /ai-models/{id}.
func (a *API) GetAIModel(ctx context.Context, id string) (Record, error) {
	rsp, err := a.raw.AiModelsGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	return decodeRecord("GET /ai-models/"+id, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// CreateAIModel sends POST /ai-models (a catalogue row; nothing is pulled).
func (a *API) CreateAIModel(ctx context.Context, body map[string]any) (Record, error) {
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.AiModelsCreateWithBodyWithResponse(ctx,
		&AiModelsCreateParams{IdempotencyKey: a.idempotencyKey()}, "application/json", r)
	if err != nil {
		return nil, err
	}
	return decodeRecord("POST /ai-models", rsp.HTTPResponse, rsp.Body, http.StatusCreated)
}

// UpdateAIModel sends PATCH /ai-models/{id} (merge patch of the metadata).
func (a *API) UpdateAIModel(ctx context.Context, id string, patch Patch) (Record, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.AiModelsUpdateWithBodyWithResponse(ctx, id, MergePatchContentType, body)
	if err != nil {
		return nil, err
	}
	return decodeRecord("PATCH /ai-models/"+id, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// DeleteAIModel sends DELETE /ai-models/{id}: the catalogue row only.
func (a *API) DeleteAIModel(ctx context.Context, id string) error {
	rsp, err := a.raw.AiModelsDeleteWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /ai-models/"+id, rsp.HTTPResponse)
	}
	return nil
}

// ── node caches ──────────────────────────────────────────────────────────────

func cachePath(method, modelID, node string) string {
	return method + " /ai-models/" + url.PathEscape(modelID) + "/node-caches/" + url.PathEscape(node)
}

// GetNodeCache reads GET /ai-models/{id}/node-caches/{node}.
func (a *API) GetNodeCache(ctx context.Context, modelID, node string) (Record, error) {
	rsp, err := a.raw.AiModelsNodeCachesGetWithResponse(ctx, modelID, node)
	if err != nil {
		return nil, err
	}
	return decodeRecord(cachePath("GET", modelID, node), rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// CacheModel sends PUT /ai-models/{id}/node-caches/{node} under one
// Idempotency-Key. It returns the cache when the node already holds one
// (200), or the store run it started (202).
func (a *API) CacheModel(ctx context.Context, modelID, node string) (Record, *Accepted, error) {
	rsp, err := a.raw.AiModelsNodeCachesPutWithResponse(ctx, modelID, node,
		&AiModelsNodeCachesPutParams{IdempotencyKey: a.idempotencyKey()})
	if err != nil {
		return nil, nil, err
	}
	switch {
	case rsp.StatusCode() == http.StatusAccepted:
		acc, err := accepted(cachePath("PUT", modelID, node), rsp.HTTPResponse, rsp.JSON202)
		return nil, acc, err
	case rsp.StatusCode() == http.StatusOK:
		c, err := decodeRecord(cachePath("PUT", modelID, node), rsp.HTTPResponse, rsp.Body, http.StatusOK)
		return c, nil, err
	}
	return nil, nil, unexpected(cachePath("PUT", modelID, node), rsp.HTTPResponse)
}

// UncacheModel sends DELETE /ai-models/{id}/node-caches/{node} under one
// Idempotency-Key and returns the store run it started.
func (a *API) UncacheModel(ctx context.Context, modelID, node string) (*Accepted, error) {
	rsp, err := a.raw.AiModelsNodeCachesDeleteWithResponse(ctx, modelID, node,
		&AiModelsNodeCachesDeleteParams{IdempotencyKey: a.idempotencyKey()})
	if err != nil {
		return nil, err
	}
	if rsp.StatusCode() != http.StatusAccepted {
		return nil, unexpected(cachePath("DELETE", modelID, node), rsp.HTTPResponse)
	}
	return accepted(cachePath("DELETE", modelID, node), rsp.HTTPResponse, rsp.JSON202)
}

// GetModelStorage reads GET /ai-models/storage.
func (a *API) GetModelStorage(ctx context.Context) (Record, error) {
	rsp, err := a.raw.AiModelsStorageGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	return decodeRecord("GET /ai-models/storage", rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// ListLoadTargets reads every page of GET /ai-models/load-targets.
func (a *API) ListLoadTargets(ctx context.Context) ([]Record, error) {
	limit := a.pageSize()
	items, _, err := listPages[Record]("GET /ai-models/load-targets", func(cursor *string) (*http.Response, []byte, error) {
		rsp, err := a.raw.AiModelsLoadTargetsListWithResponse(ctx, &AiModelsLoadTargetsListParams{Limit: &limit, Cursor: cursor})
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
	return items, err
}

// ── AI Center (read-only) ────────────────────────────────────────────────────

// ListAINodes reads every page of GET /ai/nodes and whether monitoring could
// be read (false when any page says it could not).
func (a *API) ListAINodes(ctx context.Context) ([]Record, bool, error) {
	limit := a.pageSize()
	items, reachable, err := listPages[Record]("GET /ai/nodes", func(cursor *string) (*http.Response, []byte, error) {
		rsp, err := a.raw.AiNodesListWithResponse(ctx, &AiNodesListParams{Limit: &limit, Cursor: cursor})
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
	return items, reachable != nil && *reachable, err
}

// GetAINode reads GET /ai/nodes/{hostname}.
func (a *API) GetAINode(ctx context.Context, hostname string) (Record, error) {
	rsp, err := a.raw.AiNodesGetWithResponse(ctx, hostname)
	if err != nil {
		return nil, err
	}
	return decodeRecord("GET /ai/nodes/"+url.PathEscape(hostname), rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// ListDGXClusters reads every page of GET /ai/clusters.
func (a *API) ListDGXClusters(ctx context.Context, name string) ([]Record, error) {
	limit := a.pageSize()
	params := &AiClustersListParams{Limit: &limit}
	if name != "" {
		params.Name = &name
	}
	items, _, err := listPages[Record]("GET /ai/clusters", func(cursor *string) (*http.Response, []byte, error) {
		params.Cursor = cursor
		rsp, err := a.raw.AiClustersListWithResponse(ctx, params)
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
	return items, err
}

// ListLaunchCatalog reads every page of GET /ai/catalog.
func (a *API) ListLaunchCatalog(ctx context.Context, host string, enabled *bool) ([]Record, error) {
	limit := a.pageSize()
	params := &AiCatalogListParams{Limit: &limit, Enabled: enabled}
	if host != "" {
		params.Host = &host
	}
	items, _, err := listPages[Record]("GET /ai/catalog", func(cursor *string) (*http.Response, []byte, error) {
		params.Cursor = cursor
		rsp, err := a.raw.AiCatalogListWithResponse(ctx, params)
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
	return items, err
}

// IsCode reports an *APIError with that status and code.
func IsCode(err error, status int, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status && strings.EqualFold(apiErr.Code(), code)
}
