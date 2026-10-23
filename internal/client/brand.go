// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Codes the brand resources branch on.
const (
	CodeVersionMismatch    = "version_mismatch"
	CodeAssetKindConflict  = "asset_kind_conflict"
	CodeStorageUnavailable = "storage_unavailable"
)

// GetBrand reads GET /brand.
func (a *API) GetBrand(ctx context.Context) (*Brand, error) {
	rsp, err := a.raw.BrandGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /brand", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutBrand sends PUT /brand with If-Match naming version: the replace
// happens only while the brand is still at that version, and is a 412
// version_mismatch *APIError otherwise. It is never retried on a 412.
func (a *API) PutBrand(ctx context.Context, body BrandPut, version int) (*Brand, error) {
	etag := `"` + strconv.Itoa(version) + `"`
	rsp, err := a.raw.BrandPutWithResponse(ctx, &BrandPutParams{IfMatch: &etag}, body)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("PUT /brand", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// GetBrandAsset reads GET /brand/assets/{id}.
func (a *API) GetBrandAsset(ctx context.Context, id string) (*BrandAsset, error) {
	rsp, err := a.raw.BrandAssetsGetWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /brand/assets/"+url.PathEscape(id), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// ListBrandAssets reads every page of GET /brand/assets, optionally of one
// kind or one sha256.
func (a *API) ListBrandAssets(ctx context.Context, kind, sha256 string) ([]BrandAsset, error) {
	params := &BrandAssetsListParams{}
	if kind != "" {
		k := BrandAssetsListParamsKind(kind)
		params.Kind = &k
	}
	if sha256 != "" {
		params.Sha256 = &sha256
	}
	limit := a.pageSize()
	params.Limit = &limit
	var all []BrandAsset
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /brand/assets: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.BrandAssetsListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /brand/assets", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		if next := rsp.JSON200.NextCursor; next != nil && *next != "" {
			params.Cursor = next
			continue
		}
		return all, nil
	}
}

// UploadBrandAsset sends POST /brand/assets as JSON with the content in
// base64. Bytes already stored as the same kind return that asset (created
// false); as the other kind they are a 409 asset_kind_conflict *APIError.
func (a *API) UploadBrandAsset(ctx context.Context, kind string, content []byte, filename string) (*BrandAsset, bool, error) {
	body := BrandAssetUpload{Kind: BrandAssetUploadKind(kind), ContentBase64: base64.StdEncoding.EncodeToString(content)}
	if filename != "" {
		body.Filename = &filename
	}
	rsp, err := a.raw.BrandAssetsCreateWithResponse(ctx, &BrandAssetsCreateParams{IdempotencyKey: a.idempotencyKey()}, body)
	if err != nil {
		return nil, false, err
	}
	switch {
	case rsp.JSON201 != nil:
		return rsp.JSON201, true, nil
	case rsp.JSON200 != nil:
		return rsp.JSON200, false, nil
	}
	return nil, false, unexpected("POST /brand/assets", rsp.HTTPResponse)
}

// Brand Center application profiles (platform 1.0.305 on): one profile per
// application slug, every field optional (null = inherit this platform's
// brand). PUT replaces the whole profile; with a version it is conditional
// (If-Match), without one the last write wins and a missing profile is
// created.

// GetBrandApp reads GET /brand/apps/{slug}: a 404 *APIError when the
// application has no profile.
func (a *API) GetBrandApp(ctx context.Context, slug string) (*BrandAppProfile, error) {
	rsp, err := a.raw.BrandAppsGetWithResponse(ctx, slug)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /brand/apps/"+url.PathEscape(slug), rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}

// PutBrandApp sends PUT /brand/apps/{slug}. With version set the replace
// happens only while the profile is still at that version (412
// version_mismatch otherwise, never retried); without it the profile is
// created or replaced unconditionally.
func (a *API) PutBrandApp(ctx context.Context, slug string, body BrandAppPut, version *int) (*BrandAppProfile, error) {
	params := &BrandAppsPutParams{}
	if version != nil {
		etag := `"` + strconv.Itoa(*version) + `"`
		params.IfMatch = &etag
	}
	rsp, err := a.raw.BrandAppsPutWithResponse(ctx, slug, params, body)
	if err != nil {
		return nil, err
	}
	switch {
	case rsp.JSON201 != nil:
		return rsp.JSON201, nil
	case rsp.JSON200 != nil:
		return rsp.JSON200, nil
	}
	return nil, unexpected("PUT /brand/apps/"+url.PathEscape(slug), rsp.HTTPResponse)
}

// DeleteBrandApp sends DELETE /brand/apps/{slug}: the application inherits
// this platform's brand again. A 404 *APIError when it had no profile.
func (a *API) DeleteBrandApp(ctx context.Context, slug string) error {
	rsp, err := a.raw.BrandAppsDeleteWithResponse(ctx, slug)
	if err != nil {
		return err
	}
	if rsp.StatusCode() != http.StatusNoContent {
		return unexpected("DELETE /brand/apps/"+url.PathEscape(slug), rsp.HTTPResponse)
	}
	return nil
}

// GetResolvedBrandApp reads GET /brand/apps/{slug}/resolved: what the
// application renders, with or without a profile.
func (a *API) GetResolvedBrandApp(ctx context.Context, slug string) (*ResolvedAppBrand, error) {
	rsp, err := a.raw.BrandAppsResolvedWithResponse(ctx, slug)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /brand/apps/"+url.PathEscape(slug)+"/resolved", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}
