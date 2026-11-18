resource "codigate_agent" "home" {
  name = "home-lab"

  # Change this value to rotate the agent token in place.
  rotate_trigger = "2026-01"
}

output "agent_token" {
  value     = codigate_agent.home.token
  sensitive = true
}
