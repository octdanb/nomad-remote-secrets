# Read-only visibility for humans in the Nomad UI: jobs, allocations,
# logs metadata, and node status — no submit, no exec, no secrets.
namespace "*" {
  policy = "read"
}

node {
  policy = "read"
}
