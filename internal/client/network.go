// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"net/url"
)

// GetProjectNetwork reads GET /projects/{id}/network (platform 1.0.401): the
// project's network as the platform derives and applies it, read only. A 404
// *APIError means the project is gone or out of the token's reach.
func (a *API) GetProjectNetwork(ctx context.Context, projectID string) (*ProjectNetwork, error) {
	rsp, err := a.raw.ProjectNetworkGetWithResponse(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /projects/"+url.PathEscape(projectID)+"/network", rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}
