// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Timestamps are strings in the state, written by the provider as RFC 3339 in
// UTC (`2026-09-30T10:00:00Z`, fractional seconds only when there are any).
// They are compared as INSTANTS, never as strings: `...Z` and `...+00:00`,
// or `.5Z` and `.500000Z`, are the same point in time, so a change of
// representation (another provider release, another platform release, a
// state written elsewhere) never shows as a difference and never makes a
// result "inconsistent". Two different instants are different, however they
// are written.

var (
	_ basetypes.StringTypable                    = TimestampType{}
	_ basetypes.StringValuableWithSemanticEquals = TimestampValue{}
)

// TimestampType is the attribute type of every *_at attribute.
type TimestampType struct {
	basetypes.StringType
}

func (t TimestampType) String() string { return "provider.TimestampType" }

func (t TimestampType) Equal(o attr.Type) bool {
	_, ok := o.(TimestampType)
	return ok
}

func (t TimestampType) ValueType(context.Context) attr.Value { return TimestampValue{} }

func (t TimestampType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return TimestampValue{StringValue: in}, nil
}

func (t TimestampType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	v, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	s, ok := v.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", v)
	}
	return TimestampValue{StringValue: s}, nil
}

// TimestampValue is a point in time held as a string.
type TimestampValue struct {
	basetypes.StringValue
}

func (v TimestampValue) Type(context.Context) attr.Type { return TimestampType{} }

func (v TimestampValue) Equal(o attr.Value) bool {
	other, ok := o.(TimestampValue)
	return ok && v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals reports whether two timestamps name the same instant.
// A value that is not a timestamp at all is compared as a string.
func (v TimestampValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	nv, ok := newValuable.(TimestampValue)
	if !ok {
		diags.AddError("Semantic equality check error",
			fmt.Sprintf("Expected a TimestampValue, got %T. This is a bug in the provider.", newValuable))
		return false, diags
	}
	return SameInstant(v.ValueString(), nv.ValueString()), diags
}

// NewTimestamp is the state value of t: RFC 3339 in UTC.
func NewTimestamp(t time.Time) TimestampValue {
	return TimestampValue{StringValue: basetypes.NewStringValue(FormatTimestamp(t))}
}

// NewTimestampPointer is NewTimestamp, or null for nil.
func NewTimestampPointer(t *time.Time) TimestampValue {
	if t == nil {
		return TimestampNull()
	}
	return NewTimestamp(*t)
}

// TimestampNull is a null timestamp.
func TimestampNull() TimestampValue {
	return TimestampValue{StringValue: basetypes.NewStringNull()}
}

// FormatTimestamp renders t as the provider stores it.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// ParseTimestamp reads RFC 3339 with any offset and any sub-second precision,
// and the "2026-09-30 10:00:00.123456+00:00" form an older platform release
// sent.
func ParseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) > 10 && s[10] == ' ' {
		s = s[:10] + "T" + s[11:]
	}
	return time.Parse(time.RFC3339Nano, s)
}

// SameInstant reports whether a and b are the same instant; two strings that
// are not both timestamps are compared as strings.
func SameInstant(a, b string) bool {
	ta, errA := ParseTimestamp(a)
	tb, errB := ParseTimestamp(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ta.Equal(tb)
}
