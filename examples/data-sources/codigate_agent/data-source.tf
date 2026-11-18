data "codigate_agent" "home" {
  name = "home-lab"
}

output "home_lab_connected" {
  value = data.codigate_agent.home.connected
}
