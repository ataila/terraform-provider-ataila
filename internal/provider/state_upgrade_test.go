// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

// v0States are states of 0.6.0 (schema version 0), with the attribute names
// of then, as 0.6.0 wrote them.
var v0States = map[string]struct {
	state   map[string]any
	renamed map[string]string // old name -> new name, checked after the upgrade
}{
	"ataila_user": {
		state: map[string]any{"id": "00000000-0000-4000-8000-000000000010", "email": "dana@example.com",
			"first_name": "Dana", "keycloak_linked": true, "kc_sync_status": "ok", "is_active": true},
		renamed: map[string]string{"keycloak_linked": "sso_linked", "kc_sync_status": "sso_sync_status"},
	},
	"ataila_project": {
		state: map[string]any{"id": "4", "short_name": "shop", "long_name": "Example Shop", "enable_minio": true,
			"enable_redis": false, "prod_minio_node_count": 2, "prod_minio_disks_per_vm": 2,
			"enable_dr_minio_mirror": true, "enable_synology_minio_replication": false,
			"harbor_namespace": "example-shop",
			"vault_paths":      []any{map[string]any{"env": "dev", "path": "projects/example/shop/dev"}}},
		renamed: map[string]string{"enable_minio": "enable_object_storage", "enable_redis": "enable_cache",
			"prod_minio_node_count":             "prod_object_storage_node_count",
			"prod_minio_disks_per_vm":           "prod_object_storage_disks_per_vm",
			"enable_dr_minio_mirror":            "enable_dr_object_storage_mirror",
			"enable_synology_minio_replication": "enable_nas_object_storage_replication",
			"harbor_namespace":                  "image_registry_namespace", "vault_paths": "secret_paths"},
	},
	"ataila_ai_gateway_key": {
		state: map[string]any{"id": "00000000-0000-4000-8000-000000000020", "app": "chatbot",
			"vault_path": "platform/keys/example", "vault_field": "value"},
		renamed: map[string]string{"vault_path": "secret_path", "vault_field": "secret_field"},
	},
	"ataila_ai_model": {
		state: map[string]any{"id": "3", "repo": "example-lab/model", "synology_volume": "models-2",
			"synology_path": "models/example-lab/model"},
		renamed: map[string]string{"synology_volume": "nas_volume", "synology_path": "nas_path"},
	},
}

func TestStateUpgradeRenamesVersion0(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(provider.New(testVersion)())()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for typ, c := range v0States {
		s := schemas.ResourceSchemas[typ]
		if s == nil || s.Version != 1 {
			t.Errorf("%s: schema version %v, want 1", typ, s)
			continue
		}
		raw, _ := json.Marshal(c.state)
		resp, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
			TypeName: typ, Version: 0, RawState: &tfprotov6.RawState{JSON: raw},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range resp.Diagnostics {
			t.Errorf("%s: %s: %s", typ, d.Summary, d.Detail)
		}
		if resp.UpgradedState == nil {
			continue
		}
		objType := s.ValueType()
		v, err := resp.UpgradedState.Unmarshal(objType)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		var attrs map[string]tftypes.Value
		if err := v.As(&attrs); err != nil {
			t.Fatal(err)
		}
		for old, nu := range c.renamed {
			if _, still := attrs[old]; still {
				t.Errorf("%s: %s is still in the schema", typ, old)
			}
			if attrs[nu].IsNull() {
				t.Errorf("%s: %s (was %s) is null after the upgrade", typ, nu, old)
			}
		}
		if attrs["id"].IsNull() {
			t.Errorf("%s: id lost", typ)
		}
	}

	// A state that is already of version 1 is not touched, and an unchanged
	// resource has no version to upgrade from.
	for typ, s := range schemas.ResourceSchemas {
		_, renamed := v0States[typ]
		if !renamed && s.Version != 0 {
			t.Errorf("%s: schema version %d, but nothing of it was renamed", typ, s.Version)
		}
	}
}
