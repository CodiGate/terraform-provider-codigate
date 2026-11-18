data "codigate_app" "wiki" {
  hostname = "wiki.example.com"
}

output "wiki_live" {
  value = data.codigate_app.wiki.live
}
