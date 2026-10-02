// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A respelling the platform reads as the same value is no change; anything
// else is.
func TestQuotaSame(t *testing.T) {
	for _, c := range []struct {
		key, a, b string
		want      bool
	}{
		{"pods", "40", "40", true},
		{"pods", "040", "40", true},
		{"gpu_exclusive", "00", "0", true},
		{"pvc", " 12 ", "12", true},
		{"pods", "41", "40", false},
		{"req_mem", "16Gi", "16Gi", true},
		{"req_mem", "16384Mi", "16Gi", false}, // a quantity is compared as written
		{"req_cpu", "08", "8", false},         // not a count key
		{"fair_weight", "1.0", "1", false},
		{"pods", "", "0", false},
	} {
		if got := quotaSame(c.key, c.a, c.b); got != c.want {
			t.Errorf("quotaSame(%q, %q, %q) = %v, want %v", c.key, c.a, c.b, got, c.want)
		}
	}
}

// quotaEnv builds one environment's block of the resource's type.
func quotaEnv(t *testing.T, values map[string]string, reason *string, overridden ...string) types.Object {
	t.Helper()
	v := map[string]attr.Value{
		"namespace": types.StringValue("shop-prod"), "cluster": types.StringValue("c"), "tier": types.StringValue("prod"),
		"kueue": types.BoolValue(true), "gpu_enabled": types.BoolValue(false), "overridden": types.BoolValue(len(overridden) > 0),
	}
	keys := make([]attr.Value, 0, len(overridden))
	for _, k := range overridden {
		keys = append(keys, types.StringValue(k))
	}
	v["overridden_keys"] = types.ListValueMust(types.StringType, keys)
	for _, k := range quotaKeys {
		v[k] = types.StringValue("0")
		if s, ok := values[k]; ok {
			v[k] = types.StringValue(s)
		}
	}
	v["reason"] = types.StringNull()
	if reason != nil {
		v["reason"] = types.StringValue(*reason)
	}
	return types.ObjectValueMust(quotaEnvTypes(true), v)
}

func quotaOf(t *testing.T, prod types.Object) types.Object {
	t.Helper()
	null := types.ObjectNull(quotaEnvTypes(true))
	return types.ObjectValueMust(quotaType(true).AttrTypes, map[string]attr.Value{"dev": null, "uat": null, "prod": prod})
}

func ptr(s string) *string { return &s }

// The requests a plan needs: changed keys with the reason; a respelling
// needs none; a new reason alone re-sends the overridden keys where there is
// an override, and needs no request where there is none.
func TestQuotaPatches(t *testing.T) {
	state := quotaOf(t, quotaEnv(t, map[string]string{"pods": "40", "gpu_exclusive": "1"}, ptr("training"),
		"pods", "gpu_exclusive"))

	changed := quotaPatches(quotaOf(t, quotaEnv(t, map[string]string{"pods": "50", "gpu_exclusive": "1"},
		ptr("training"), "pods", "gpu_exclusive")), state)
	if p := changed["prod"]; len(p) != 2 || p["pods"] != "50" || p["reason"] != "training" {
		t.Errorf("changed key: %v, want pods 50 with the reason", changed)
	}

	respelt := quotaPatches(quotaOf(t, quotaEnv(t, map[string]string{"pods": "040", "gpu_exclusive": "01"},
		ptr("training"), "pods", "gpu_exclusive")), state)
	if len(respelt) != 0 {
		t.Errorf("respelling: %v, want no request", respelt)
	}

	reasoned := quotaPatches(quotaOf(t, quotaEnv(t, map[string]string{"pods": "40", "gpu_exclusive": "1"},
		ptr("retraining"), "pods", "gpu_exclusive")), state)
	if p := reasoned["prod"]; len(p) != 3 || p["pods"] != "40" || p["gpu_exclusive"] != "1" || p["reason"] != "retraining" {
		t.Errorf("new reason: %v, want the overridden keys again with it", reasoned)
	}

	bare := quotaOf(t, quotaEnv(t, nil, nil))
	noRow := quotaPatches(quotaOf(t, quotaEnv(t, nil, ptr("why"))), bare)
	if len(noRow) != 0 {
		t.Errorf("new reason without an override: %v, want no request", noRow)
	}

	defaulted := quotaPatches(quotaOf(t, quotaEnv(t, map[string]string{"pods": "25"}, nil)), bare)
	if p := defaulted["prod"]; p["reason"] != defaultQuotaReason {
		t.Errorf("no reason given: %v, want the default text", defaulted)
	}
}
