# By id, by e-mail address or by username. OpenTofu:
tofu import ataila_user.dana 8b2e6f10-5a7c-4e3d-b9f2-0d4c6a8e1b37
tofu import ataila_user.dana email:dana.example@example.com
tofu import ataila_user.dana username:dana.example

# Terraform:
terraform import ataila_user.dana 8b2e6f10-5a7c-4e3d-b9f2-0d4c6a8e1b37
terraform import ataila_user.dana email:dana.example@example.com
terraform import ataila_user.dana username:dana.example
