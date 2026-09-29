// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

func TestAccMetaDataSource(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: metaConfig,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_meta.this", "api_version", "1.0.0"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "platform_version", "1.0.0"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "tier", "standard"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "tenancy_mode", "multi"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "modules.#", "2"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "modules.0", "ai-gateway"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "modules.1", "sp-mode"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "licence.state", "ACTIVE"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "licence.state_reason", ""),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "licence.days_remaining", "120"),
			),
		}},
	})
}

// An unlicensed platform: empty tier and tenancy mode, no modules, and no
// expiry to count down to.
func TestAccMetaDataSource_Unlicensed(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	m.SetMeta(func(meta map[string]any) {
		meta["tier"] = ""
		meta["tenancy_mode"] = ""
		meta["modules"] = []string{}
		meta["licence"] = map[string]any{"state": "UNLICENSED", "state_reason": "no licence installed", "days_remaining": nil}
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: metaConfig,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_meta.this", "tier", ""),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "modules.#", "0"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "licence.state", "UNLICENSED"),
				resource.TestCheckResourceAttr("data.ataila_meta.this", "licence.state_reason", "no licence installed"),
				resource.TestCheckNoResourceAttr("data.ataila_meta.this", "licence.days_remaining"),
			),
		}},
	})
}
