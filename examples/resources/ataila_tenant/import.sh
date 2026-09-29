# By id, or by slug. OpenTofu:
tofu import ataila_tenant.builds 3f0c9a52-4d1e-4b8a-9d61-2f5b7e0c1a44
tofu import ataila_tenant.builds slug:example-builds

# Terraform:
terraform import ataila_tenant.builds 3f0c9a52-4d1e-4b8a-9d61-2f5b7e0c1a44
terraform import ataila_tenant.builds slug:example-builds
