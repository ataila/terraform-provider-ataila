// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"strconv"
)

// The Feedback & Roadmap backlog (platform 1.0.260 on), read only, on the
// platform that issues releases. GET /feedback/{n}/brief is NEVER called: the
// platform records that fetch and moves the item to in_progress.

// FeedbackFilter narrows GET /feedback; empty fields do not filter.
type FeedbackFilter struct {
	Status, Kind, Customer, Week, Source string
}

// ListFeedback reads every page of GET /feedback.
func (a *API) ListFeedback(ctx context.Context, f FeedbackFilter) ([]FeedbackSummary, error) {
	limit := a.pageSize()
	params := &FeedbackListParams{Limit: &limit}
	if f.Status != "" {
		s := FeedbackListParamsStatus(f.Status)
		params.Status = &s
	}
	if f.Kind != "" {
		k := FeedbackListParamsKind(f.Kind)
		params.Kind = &k
	}
	if f.Customer != "" {
		params.Customer = &f.Customer
	}
	if f.Week != "" {
		params.Week = &f.Week
	}
	if f.Source != "" {
		s := FeedbackListParamsSource(f.Source)
		params.Source = &s
	}
	var all []FeedbackSummary
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /feedback: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.FeedbackListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /feedback", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		if next := rsp.JSON200.NextCursor; next != nil && *next != "" {
			params.Cursor = next
			continue
		}
		return all, nil
	}
}

// GetFeedback reads GET /feedback/{number}: the item with its thread.
func (a *API) GetFeedback(ctx context.Context, number int) (*FeedbackItem, error) {
	n := strconv.Itoa(number)
	rsp, err := a.raw.FeedbackGetWithResponse(ctx, n)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /feedback/"+n, rsp.HTTPResponse)
	}
	return rsp.JSON200, nil
}
