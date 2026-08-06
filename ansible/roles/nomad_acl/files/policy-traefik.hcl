# Traefik's Nomad provider only reads service registrations; read-only
# namespace access covers the services and jobs APIs it uses.
namespace "*" {
  policy = "read"
}
