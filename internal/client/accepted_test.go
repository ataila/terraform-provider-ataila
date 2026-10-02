// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"strings"
	"testing"
)

const acceptedBody = `{"id":"release:7","kind":"release","status":"running","created_at":"2026-10-01T10:00:00Z"}`

func TestAcceptedReadsLocationAndReplay(t *testing.T) {
	for _, c := range []struct {
		name     string
		headers  map[string]string
		body     string
		wantID   string
		replayed bool
		wantErr  string
	}{
		{name: "first answer", headers: map[string]string{"Location": "/api/v1/operations/release:7"},
			body: acceptedBody, wantID: "release:7"},
		{name: "replayed", headers: map[string]string{"Location": "/api/v1/operations/release:7", "Idempotent-Replayed": "true"},
			body: acceptedBody, wantID: "release:7", replayed: true},
		{name: "absolute URL", headers: map[string]string{"Location": "https://portal.example.com/api/v1/operations/release%3A7"},
			body: acceptedBody, wantID: "release:7"},
		{name: "no Location", body: acceptedBody, wantID: "release:7"},
		{name: "Location without a body id", headers: map[string]string{"Location": "/api/v1/operations/release:7"},
			body: `{"kind":"release","status":"running","created_at":"2026-10-01T10:00:00Z"}`, wantID: "release:7"},
		{name: "Location and body disagree", headers: map[string]string{"Location": "/api/v1/operations/release:8"},
			body: acceptedBody, wantErr: `names operation "release:8", the answer's body "release:7"`},
		{name: "Location names no operation", headers: map[string]string{"Location": "/api/v1/release-operations/7"},
			body: acceptedBody, wantErr: "names no operation"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripted{replies: []reply{{status: 202, headers: c.headers, body: c.body}}}
			api, _ := newTestAPI(t, s)
			acc, err := api.RequestPromotion(context.Background(), "1", ReleasePromotionCreate{Component: "app-api", TargetEnv: "dev"})
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error %v, want one containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if acc.Operation.Id != c.wantID || acc.Replayed != c.replayed {
				t.Errorf("accepted %+v (operation %q), want %q replayed %v", acc, acc.Operation.Id, c.wantID, c.replayed)
			}
		})
	}
}
