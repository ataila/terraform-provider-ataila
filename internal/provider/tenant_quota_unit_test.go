// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

func quotaEntry(limit float64, policy string, note *string) types.Object {
	n := types.StringNull()
	if note != nil {
		n = types.StringValue(*note)
	}
	return types.ObjectValueMust(quotaEntryTypes, map[string]attr.Value{
		"limit": types.Float64Value(limit), "policy": types.StringValue(policy), "note": n})
}

func quotaMap(entries map[string]types.Object) types.Map {
	elems := map[string]attr.Value{}
	for k, v := range entries {
		elems[k] = v
	}
	return types.MapValueMust(types.ObjectType{AttrTypes: quotaEntryTypes}, elems)
}

// The PUT body is the planned map in the platform's dimension order, the
// policy defaulted, a null note left out.
func TestQuotaLimitsFromPlan(t *testing.T) {
	note := "demo"
	got := quotaLimitsFromPlan(quotaMap(map[string]types.Object{
		"gpu":    quotaEntry(0, "hard_cap", nil),
		"ai_tpm": quotaEntry(200000, "", &note),
		"vcpu":   quotaEntry(16, "auto", nil),
	}))
	b, _ := json.Marshal(got)
	want := `[{"dimension":"vcpu","limit":16,"policy":"auto"},` +
		`{"dimension":"ai_tpm","limit":200000,"policy":"auto","note":"demo"},` +
		`{"dimension":"gpu","limit":0,"policy":"hard_cap"}]`
	if string(b) != want {
		t.Errorf("body = %s\nwant   %s", b, want)
	}
	if got := quotaLimitsFromPlan(quotaMap(nil)); got == nil || len(got) != 0 {
		t.Errorf("an empty map must send [] (every limit removed), got %#v", got)
	}
}

// The platform keeps four decimal places: the configured spelling of a limit
// it rounded is kept; a different limit is the platform's.
func TestQuotaMapKeepsTheConfiguredSpelling(t *testing.T) {
	prior := quotaMap(map[string]types.Object{
		"ai_budget_eur_month": quotaEntry(10.123456, "auto", nil),
		"ai_tpm":              quotaEntry(100, "auto", nil),
	})
	got := quotaMapValue([]client.TenantQuotaRow{
		{Dimension: "ai_budget_eur_month", Limit: 10.1235, Policy: "auto"},
		{Dimension: "ai_tpm", Limit: 200, Policy: "auto"},
	}, prior)
	limit := func(dim string) float64 {
		return got.Elements()[dim].(types.Object).Attributes()["limit"].(types.Float64).ValueFloat64()
	}
	if limit("ai_budget_eur_month") != 10.123456 {
		t.Errorf("a rounded limit lost the configured spelling: %v", limit("ai_budget_eur_month"))
	}
	if limit("ai_tpm") != 200 {
		t.Errorf("a changed limit kept the prior value: %v", limit("ai_tpm"))
	}
	for _, c := range []struct {
		a, b float64
		want bool
	}{{10.123456, 10.1235, true}, {1, 1.0001, false}, {0, 0.00004, true}, {5, 5, true}} {
		if sameLimit(c.a, c.b) != c.want {
			t.Errorf("sameLimit(%v, %v) = %v", c.a, c.b, !c.want)
		}
	}
}

// The dimensions the apply removes are the state's keys the plan lacks.
func TestRemovedDimensions(t *testing.T) {
	state := quotaMap(map[string]types.Object{"vcpu": quotaEntry(1, "auto", nil), "gpu": quotaEntry(1, "auto", nil),
		"ai_tpm": quotaEntry(1, "auto", nil)})
	plan := quotaMap(map[string]types.Object{"ai_tpm": quotaEntry(2, "auto", nil)})
	if got := fmt.Sprint(removedDimensions(state, plan)); got != "[gpu vcpu]" {
		t.Errorf("removed = %s", got)
	}
	if got := removedDimensions(state, types.MapUnknown(types.ObjectType{AttrTypes: quotaEntryTypes})); got != nil {
		t.Errorf("an unknown plan removes nothing yet, got %v", got)
	}
}

// JSON text is the same for the same value, however the platform ordered it.
func TestJSONText(t *testing.T) {
	a := jsonText(json.RawMessage(`{"b": 1, "a": {"y": 2, "x": [1, "z"]}}`))
	b := jsonText(json.RawMessage(`{"a":{"x":[1,"z"],"y":2},"b":1}`))
	if !a.Equal(b) || a.ValueString() != `{"a":{"x":[1,"z"],"y":2},"b":1}` {
		t.Errorf("jsonText: %s vs %s", a, b)
	}
	for _, raw := range []string{"", "null", "  "} {
		if !jsonText(json.RawMessage(raw)).IsNull() {
			t.Errorf("jsonText(%q) is not null", raw)
		}
	}
	if got := jsonMember(json.RawMessage(`{"key_alias":"x","key_id":""}`), "key_alias"); got.ValueString() != "x" {
		t.Errorf("jsonMember = %s", got)
	}
	if !jsonMember(json.RawMessage(`{"key_id":""}`), "key_id").IsNull() {
		t.Error("an empty member must be null")
	}
}

// The provider's vocabularies are the contract's.
func TestOrderVocabulariesAreTheContracts(t *testing.T) {
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []any `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	s := spec.Components.Schemas
	for name, c := range map[string]struct {
		got  []string
		want []any
	}{
		"dimension": {quotaDimensions, s["TenantQuotaIn"].Properties["dimension"].Enum},
		"policy":    {quotaPolicies, s["TenantQuotaIn"].Properties["policy"].Enum},
		"status":    {orderStatuses, s["Order"].Properties["status"].Enum},
	} {
		if fmt.Sprint(c.got) != fmt.Sprint(c.want) {
			t.Errorf("%s: provider %v, contract %v", name, c.got, c.want)
		}
	}
}
