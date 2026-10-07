// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// 0.7.0 renamed attributes that named an internal system (contract 1.0.176).
// The resources that had one are at schema version 1, and upgrade a version 0
// state by renaming its attributes; nothing else changes and nothing is read
// from the platform.

// Renames of version 0 to version 1, per resource type.
var (
	userRenamesV1 = map[string]string{
		"keycloak_linked": "sso_linked",
		"kc_sync_status":  "sso_sync_status",
	}
	projectRenamesV1 = map[string]string{
		"enable_minio":                      "enable_object_storage",
		"enable_redis":                      "enable_cache",
		"prod_minio_node_count":             "prod_object_storage_node_count",
		"prod_minio_disks_per_vm":           "prod_object_storage_disks_per_vm",
		"enable_dr_minio_mirror":            "enable_dr_object_storage_mirror",
		"enable_synology_minio_replication": "enable_nas_object_storage_replication",
		"harbor_namespace":                  "image_registry_namespace",
		"vault_paths":                       "secret_paths",
	}
	gatewayKeyRenamesV1 = map[string]string{
		"vault_path":  "secret_path",
		"vault_field": "secret_field",
	}
	aiModelRenamesV1 = map[string]string{
		"synology_volume": "nas_volume",
		"synology_path":   "nas_path",
	}
)

// renameUpgraders upgrades a version 0 state by renaming top-level
// attributes. It works on the state's JSON, so no copy of the old schema is
// kept.
func renameUpgraders(renames map[string]string) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: func(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			if req.RawState == nil || req.RawState.JSON == nil {
				resp.Diagnostics.AddError("Cannot upgrade the state",
					"The stored state of schema version 0 is not JSON; the provider cannot rename its attributes. "+
						"Remove the resource from the state and import it again.")
				return
			}
			out, err := renameJSON(req.RawState.JSON, renames)
			if err != nil {
				resp.Diagnostics.AddError("Cannot upgrade the state",
					fmt.Sprintf("The stored state of schema version 0 cannot be read: %v. Remove the resource from "+
						"the state and import it again.", err))
				return
			}
			resp.DynamicValue = &tfprotov6.DynamicValue{JSON: out}
		}},
	}
}

// renameJSON renames the top-level members of a JSON object. An old name that
// is absent is skipped; a new name that is already present wins.
func renameJSON(raw []byte, renames map[string]string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	olds := make([]string, 0, len(renames))
	for old := range renames {
		olds = append(olds, old)
	}
	sort.Strings(olds)
	for _, old := range olds {
		v, ok := obj[old]
		if !ok {
			continue
		}
		delete(obj, old)
		if _, taken := obj[renames[old]]; !taken {
			obj[renames[old]] = v
		}
	}
	return json.Marshal(obj)
}
