// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Codes of a 503 that means the gateway is absent or unusable on this
// platform. They are final: some platforms have no gateway at all, and
// retrying cannot give them one.
const (
	CodeGatewayNotConfigured   = "gateway_not_configured"
	CodeGatewayUnreachable     = "gateway_unreachable"
	CodeSecretStoreWriteFailed = "secret_store_write_failed"
	CodeKeyAdopted             = "key_adopted"
	CodeKeyMissingOnGateway    = "key_missing_on_gateway"
	CodeModelNotLoaded         = "model_not_loaded"
)

// finalUnavailable lists the 503 codes that are not retried.
var finalUnavailable = map[string]bool{
	CodeGatewayNotConfigured:   true,
	CodeGatewayUnreachable:     true,
	CodeSecretStoreWriteFailed: true,
	CodeStorageUnavailable:     true,
	CodeLoadedStateUnknown:     true,
}

// GatewayKeyData is a virtual key as the API answers it, decoded by hand so
// that money keeps float64 precision (the generated model uses float32, which
// would turn a configured 0.1 into 0.10000000149).
type GatewayKeyData struct {
	Id              string       `json:"id"`
	OrganizationId  string       `json:"organization_id"`
	ProjectId       *string      `json:"project_id"`
	Env             string       `json:"env"`
	App             string       `json:"app"`
	Feature         *string      `json:"feature"`
	KeyAlias        string       `json:"key_alias"`
	Models          []string     `json:"models"`
	RpmLimit        *int64       `json:"rpm_limit"`
	TpmLimit        *int64       `json:"tpm_limit"`
	SoftBudgetUsd   *float64     `json:"soft_budget_usd"`
	BudgetDuration  *string      `json:"budget_duration"`
	SpendUsd        *float64     `json:"spend_usd"`
	Live            string       `json:"live"`
	SecretPath      *string      `json:"secret_path"`
	SecretField     *string      `json:"secret_field"`
	Origin          string       `json:"origin"`
	TokenHashPrefix *string      `json:"token_hash_prefix"`
	CreatedBy       *string      `json:"created_by"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	RotatedAt       *time.Time   `json:"rotated_at"`
	Secret          *string      `json:"secret"`
	Warnings        []ApiWarning `json:"warnings"`
}

// GatewayKeyFilter narrows GET /ai/gateway/keys.
type GatewayKeyFilter struct {
	OrganizationID string
	Env            string
	App            string
	Origin         string
	KeyAlias       string
}

func decodeKey(op string, resp *http.Response, body []byte, want int) (*GatewayKeyData, error) {
	if resp == nil || resp.StatusCode != want {
		return nil, unexpected(op, resp)
	}
	var k GatewayKeyData
	if err := json.Unmarshal(body, &k); err != nil {
		return nil, fmt.Errorf("%s: decoding the key: %w", op, err)
	}
	return &k, nil
}

func jsonBody(v any) (*bytes.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding the request body: %w", err)
	}
	return bytes.NewReader(b), nil
}

// GetGateway reads GET /ai/gateway.
func (a *API) GetGateway(ctx context.Context) (*AiGateway, error) {
	rsp, err := a.raw.AiGatewayGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /ai/gateway", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// ListGatewayKeys reads every page of GET /ai/gateway/keys (registry data).
func (a *API) ListGatewayKeys(ctx context.Context, f GatewayKeyFilter) ([]GatewayKeyData, error) {
	params := &AiGatewayKeysListParams{}
	if f.OrganizationID != "" {
		params.OrganizationId = &f.OrganizationID
	}
	if f.Env != "" {
		e := AiGatewayKeysListParamsEnv(f.Env)
		params.Env = &e
	}
	if f.App != "" {
		params.App = &f.App
	}
	if f.Origin != "" {
		o := AiGatewayKeysListParamsOrigin(f.Origin)
		params.Origin = &o
	}
	if f.KeyAlias != "" {
		params.KeyAlias = &f.KeyAlias
	}
	limit := a.pageSize()
	params.Limit = &limit
	var all []GatewayKeyData
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /ai/gateway/keys: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.AiGatewayKeysListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.StatusCode() != http.StatusOK {
			return nil, unexpected("GET /ai/gateway/keys", rsp.HTTPResponse)
		}
		var pg struct {
			Items      []GatewayKeyData `json:"items"`
			NextCursor *string          `json:"next_cursor"`
		}
		if err := json.Unmarshal(rsp.Body, &pg); err != nil {
			return nil, fmt.Errorf("GET /ai/gateway/keys: %w", err)
		}
		all = append(all, pg.Items...)
		if pg.NextCursor != nil && *pg.NextCursor != "" {
			params.Cursor = pg.NextCursor
			continue
		}
		return all, nil
	}
}

// GetGatewayKey reads GET /ai/gateway/keys/{id}: live values from the gateway.
func (a *API) GetGatewayKey(ctx context.Context, id string) (*GatewayKeyData, error) {
	rsp, err := a.raw.AiGatewayKeysGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	return decodeKey("GET /ai/gateway/keys/"+id, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// CreateGatewayKey sends POST /ai/gateway/keys with a body built by the
// caller (money as float64).
func (a *API) CreateGatewayKey(ctx context.Context, body map[string]any) (*GatewayKeyData, error) {
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.AiGatewayKeysCreateWithBodyWithResponse(ctx,
		&AiGatewayKeysCreateParams{IdempotencyKey: a.idempotencyKey()}, "application/json", r)
	if err != nil {
		return nil, err
	}
	return decodeKey("POST /ai/gateway/keys", rsp.HTTPResponse, rsp.Body, http.StatusCreated)
}

// UpdateGatewayKey sends PATCH /ai/gateway/keys/{id} (JSON Merge Patch).
func (a *API) UpdateGatewayKey(ctx context.Context, id string, patch Patch) (*GatewayKeyData, error) {
	body, err := patchBody(patch)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.AiGatewayKeysUpdateWithBodyWithResponse(ctx, id, MergePatchContentType, body)
	if err != nil {
		return nil, err
	}
	return decodeKey("PATCH /ai/gateway/keys/"+id, rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// RotateGatewayKey sends POST /ai/gateway/keys/{id}/rotations.
func (a *API) RotateGatewayKey(ctx context.Context, id string, exposeSecret bool) (*GatewayKeyData, error) {
	r, err := jsonBody(map[string]any{"expose_secret": exposeSecret})
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.AiGatewayKeysRotateWithBodyWithResponse(ctx, id,
		&AiGatewayKeysRotateParams{IdempotencyKey: a.idempotencyKey()}, "application/json", r)
	if err != nil {
		return nil, err
	}
	return decodeKey("POST /ai/gateway/keys/"+id+"/rotations", rsp.HTTPResponse, rsp.Body, http.StatusOK)
}

// DeleteGatewayKey sends DELETE /ai/gateway/keys/{id}: irreversible.
func (a *API) DeleteGatewayKey(ctx context.Context, id string) error {
	rsp, err := a.raw.AiGatewayKeysDeleteWithResponse(ctx, id)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /ai/gateway/keys/"+id, rsp.HTTPResponse)
	}
	return nil
}

// ListServingTiers reads every page of GET /ai/gateway/tiers.
func (a *API) ListServingTiers(ctx context.Context) ([]ServingTier, error) {
	params := &AiGatewayTiersListParams{}
	limit := a.pageSize()
	params.Limit = &limit
	var all []ServingTier
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /ai/gateway/tiers: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.AiGatewayTiersListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /ai/gateway/tiers", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		if next := rsp.JSON200.NextCursor; next != nil && *next != "" {
			params.Cursor = next
			continue
		}
		return all, nil
	}
}

// GetServingTier reads GET /ai/gateway/tiers/{key}.
func (a *API) GetServingTier(ctx context.Context, key string) (*ServingTier, error) {
	rsp, err := a.raw.AiGatewayTiersGetWithResponse(ctx, key)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /ai/gateway/tiers/"+url.PathEscape(key), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutServingTier sends PUT /ai/gateway/tiers/{key}. A nil pinnedModel is
// sent as null: the tier returns to auto-assign.
func (a *API) PutServingTier(ctx context.Context, key string, pinnedModel *string, enabled, allowUnloadedPin bool) (*ServingTier, error) {
	body := map[string]any{"enabled": enabled, "allow_unloaded_pin": allowUnloadedPin, "pinned_model": nil}
	if pinnedModel != nil {
		body["pinned_model"] = *pinnedModel
	}
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	rsp, err := a.raw.AiGatewayTiersPutWithBodyWithResponse(ctx, key, "application/json", r)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PUT /ai/gateway/tiers/"+url.PathEscape(key), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}
