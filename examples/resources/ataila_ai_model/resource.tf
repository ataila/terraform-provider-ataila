# A catalogue row: the weights are pulled in the portal, not here.
resource "ataila_ai_model" "coder" {
  repo          = "example-lab/example-coder-32B"
  vendor        = "Example Lab"
  license       = "apache-2.0"
  param_count_b = 32
  category      = "code"
  notes         = "Candidate for the code tier."
}
