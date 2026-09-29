// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"context"
	"errors"
	"testing"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// The mock's answers must decode with the client generated from the pinned
// contract; otherwise the acceptance tests would prove the wrong thing.
func TestMockSpeaksTheContract(t *testing.T) {
	m := NewMockAPI(t)
	api, err := client.New(client.Config{Endpoint: m.URL(), Token: MockToken, CACertPEM: []byte(m.CACertPEM())})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	meta, err := api.Meta(ctx)
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if err := client.CheckAPIVersion(meta.ApiVersion); err != nil {
		t.Errorf("mock API version: %v", err)
	}
	who, err := api.Whoami(ctx)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if who.Token == nil || who.Principal.Kind != "service" {
		t.Errorf("whoami = %+v", who)
	}

	_, err = api.Operation(ctx, "nope")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsNotFound() || apiErr.Code() != "operation_not_found" || apiErr.RequestID() == "" {
		t.Errorf("unknown operation: %v", err)
	}
}

func TestMockOrderOfChecks(t *testing.T) {
	m := NewMockAPI(t)
	ctx := context.Background()
	bad, err := client.New(client.Config{Endpoint: m.URL(), Token: "wrong", CACertPEM: []byte(m.CACertPEM())})
	if err != nil {
		t.Fatal(err)
	}
	var apiErr *client.APIError

	if _, err := bad.Meta(ctx); !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Errorf("wrong token: %v", err)
	}

	m.RefuseLicence("/meta", "licence_locked", "Install a licence.")
	if _, err := bad.Meta(ctx); !errors.As(err, &apiErr) || !apiErr.IsLicenceRefusal() {
		t.Errorf("the licence gate must answer before authentication: %v", err)
	}

	m.SwitchOff()
	if _, err := bad.Meta(ctx); !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Errorf("a switched-off API must answer 404 first: %v", err)
	}
}
