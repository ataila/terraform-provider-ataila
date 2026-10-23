// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// The platform's audit log, read only (GET /audit-events). The log is large
// and grows with every change, so a read stops at a caller-given number of
// events instead of walking every page.

// AuditFilter narrows GET /audit-events; empty fields do not filter.
type AuditFilter struct {
	Actor, AuthKind, TokenID, RequestID, EntityType, EntityID, Action, TenantID string
	From, To                                                                    *time.Time
}

func (f AuditFilter) params() (*AuditEventsListParams, error) {
	p := &AuditEventsListParams{OccurredFrom: f.From, OccurredTo: f.To}
	for _, s := range []struct {
		value string
		into  **string
	}{{f.Actor, &p.Actor}, {f.RequestID, &p.RequestId}, {f.EntityType, &p.EntityType},
		{f.EntityID, &p.EntityId}, {f.Action, &p.Action}} {
		if s.value != "" {
			v := s.value
			*s.into = &v
		}
	}
	if f.AuthKind != "" {
		k := AuditEventsListParamsAuthKind(f.AuthKind)
		p.AuthKind = &k
	}
	for _, u := range []struct {
		name, value string
		into        **openapi_types.UUID
	}{{"token_id", f.TokenID, &p.TokenId}, {"tenant_id", f.TenantID, &p.TenantId}} {
		if u.value == "" {
			continue
		}
		var id openapi_types.UUID
		if err := id.UnmarshalText([]byte(u.value)); err != nil {
			return nil, fmt.Errorf("%s %q is not a UUID", u.name, u.value)
		}
		*u.into = &id
	}
	return p, nil
}

// ListAuditEvents reads GET /audit-events, newest first, until it holds max
// events or the list ends. more reports that further events matched.
func (a *API) ListAuditEvents(ctx context.Context, f AuditFilter, max int) (events []AuditEvent, more bool, err error) {
	params, err := f.params()
	if err != nil {
		return nil, false, err
	}
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, false, fmt.Errorf("GET /audit-events: gave up after %d pages; the cursor never ended", maxPages)
		}
		limit := min(max-len(events), 500)
		params.Limit = &limit
		rsp, err := a.raw.AuditEventsListWithResponse(ctx, params)
		if err != nil {
			return nil, false, err
		}
		if rsp.JSON200 == nil {
			return nil, false, unexpected("GET /audit-events", rsp.HTTPResponse)
		}
		events = append(events, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		hasNext := next != nil && *next != ""
		if len(events) >= max {
			return events[:max], hasNext || len(events) > max, nil
		}
		if !hasNext {
			return events, false, nil
		}
		params.Cursor = next
	}
}
