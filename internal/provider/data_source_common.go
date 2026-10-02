// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// providerDataFrom unpacks what Configure handed over. It is nil (with no
// error) before the provider is configured, which the framework allows.
func providerDataFrom(raw any, diags *diag.Diagnostics) *ProviderData {
	if raw == nil {
		return nil
	}
	data, ok := raw.(*ProviderData)
	if !ok {
		diags.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *provider.ProviderData, got %T. This is a bug in the provider.", raw))
		return nil
	}
	return data
}

func stringOrEmpty(p *string) types.String {
	if p == nil {
		return types.StringValue("")
	}
	return types.StringValue(*p)
}

func stringOrNull(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

// timestampString renders an optional time as the provider stores times.
func timestampString(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(FormatTimestamp(*t))
}

func stringList(ctx context.Context, items []string, diags *diag.Diagnostics) types.List {
	if items == nil {
		items = []string{}
	}
	v, d := types.ListValueFrom(ctx, types.StringType, items)
	diags.Append(d...)
	return v
}
