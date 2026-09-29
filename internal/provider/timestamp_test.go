// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func ts(s string) TimestampValue { return TimestampValue{StringValue: basetypes.NewStringValue(s)} }

func TestTimestampSemanticEquality(t *testing.T) {
	ctx := context.Background()
	same := [][2]string{
		{"2026-09-30T10:00:00Z", "2026-09-30T10:00:00+00:00"},
		{"2026-09-30T10:00:00Z", "2026-09-30T12:00:00+02:00"},
		{"2026-09-30T10:00:00.5Z", "2026-09-30T10:00:00.500000+00:00"},
		{"2026-09-30T10:00:00Z", "2026-09-30T10:00:00.000Z"},
		{"2026-09-30T10:00:00.123456Z", "2026-09-30T10:00:00.123456000Z"},
		// An older platform release's Python rendering.
		{"2026-09-30 10:00:00.123456+00:00", "2026-09-30T10:00:00.123456Z"},
	}
	for _, p := range same {
		eq, diags := ts(p[0]).StringSemanticEquals(ctx, ts(p[1]))
		if diags.HasError() || !eq {
			t.Errorf("%s and %s must be the same instant", p[0], p[1])
		}
	}
	different := [][2]string{
		{"2026-09-30T10:00:00Z", "2026-09-30T10:00:01Z"},
		// A truncated fraction is another instant, however it is written.
		{"2026-09-30T10:00:00.123456Z", "2026-09-30T10:00:00.123Z"},
		{"2026-09-30T10:00:00Z", "2026-09-30T10:00:00"}, // no offset: not RFC 3339, compared as text
		{"not a time", "2026-09-30T10:00:00Z"},
	}
	for _, p := range different {
		if eq, _ := ts(p[0]).StringSemanticEquals(ctx, ts(p[1])); eq {
			t.Errorf("%s and %s must differ", p[0], p[1])
		}
	}
	if eq, _ := ts("x").StringSemanticEquals(ctx, ts("x")); !eq {
		t.Error("equal text that is no timestamp must still be equal")
	}
}

func TestTimestampValues(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 500_000_000, time.FixedZone("CEST", 2*3600))
	if got := NewTimestamp(at).ValueString(); got != "2026-09-30T10:00:00.5Z" {
		t.Errorf("NewTimestamp = %s", got)
	}
	if !NewTimestampPointer(nil).IsNull() || !TimestampNull().IsNull() {
		t.Error("nil must be null")
	}
	ctx := context.Background()
	v, err := TimestampType{}.ValueFromTerraform(ctx, tftypes.NewValue(tftypes.String, "2026-09-30T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(TimestampValue); !ok || !v.Type(ctx).Equal(TimestampType{}) {
		t.Errorf("ValueFromTerraform gave %T", v)
	}
	if ts("a").Equal(basetypes.NewStringValue("a")) {
		t.Error("a timestamp is not equal to a plain string value")
	}
}

func TestEmailInternationalDomain(t *testing.T) {
	if !sameEmail("Pat@xn--bcher-kva.example", "Pat@bücher.example") ||
		!sameEmail("Pat@BÜCHER.example", "Pat@bücher.example") {
		t.Error("an internationalised domain is the same in both forms and any case")
	}
	if sameEmail("pat@bücher.example", "Pat@bücher.example") {
		t.Error("the local part is case-sensitive")
	}
}
