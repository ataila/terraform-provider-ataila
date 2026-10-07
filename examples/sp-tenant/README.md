# A service provider's customer tenant, from code

`main.tf` manages one customer tenant of a service-provider platform: its quota set (`ataila_tenant_quota`),
an AI gateway key inside it (`ataila_ai_gateway_key`) and its own AI rates (`ataila_ai_rate_plan`), and reads
back what it ordered (`ataila_orders`) and what it used this month, rated (`ataila_ai_usage`).

```shell
export ATAILA_ENDPOINT=https://portal.example.com
export ATAILA_TOKEN=...            # from the portal (API tokens); never in a file
tofu init && tofu apply -var tenant_slug=example -var plan_from=2026-10-01
# or: terraform init && terraform apply -var tenant_slug=example -var plan_from=2026-10-01
```

The token needs `tenancy-read-global`, `orders-admin-global` and `ai-gateway-admin-global`. Nothing here is
destroy-gated, so the token needs no `allow_destroy`: `destroy` would remove the tenant's limits, end its rate
plan and delete the key. Keep `plan_from` on the first of the month to price the whole month at the plan (usage
is rated when it is read).

## Trying a build before it is published

The registries serve released versions only. To run this example with a provider built from a checkout (a
release candidate, say), build the binary and point the CLI at it with a development override:

```shell
go build -o "$HOME/ataila-provider-dev/terraform-provider-ataila" .   # in the provider checkout
```

Then add this to `~/.tofurc` (OpenTofu) or `~/.terraformrc` (Terraform); on Windows the files are
`%APPDATA%\tofu.rc` and `%APPDATA%\terraform.rc`, and the binary is
`terraform-provider-ataila.exe`:

```hcl
provider_installation {
  dev_overrides {
    "registry.opentofu.org/ataila/ataila" = "/home/you/ataila-provider-dev"
    "registry.terraform.io/ataila/ataila" = "/home/you/ataila-provider-dev"
  }
  direct {}
}
```

With the override in place, skip `init` for this provider and run `plan` and `apply` directly; both CLIs warn
that an override is active. Remove the block to go back to the published release.
