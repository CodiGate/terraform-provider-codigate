resource "codigate_app_edge_rules" "wiki" {
  app_id          = codigate_app.wiki.id
  allow_countries = ["NL", "BE"]
  deny_ips        = ["203.0.113.0/24"]
  path_rules = [
    { path = "/admin", action = "login" },
    { path = "/.git", action = "block" },
  ]
  rate_limit_per_minute = 600
  ban = {
    after_rate_limits = 3
    window_seconds    = 600
    duration_seconds  = 3600
  }
  challenge = {
    mode            = "auto"
    auto_per_minute = 120
    # API clients and webhooks can't solve a browser challenge.
    skip_paths = ["/api/", "/webhooks/stripe"]
  }
  waf = {
    mode       = "block"
    exclusions = [942100]
    # Switch off a CRS rule on one path only, for example where a search box takes SQL-looking text.
    path_exclusions = [
      { path = "/api/search", rules = [942100, 942200] },
    ]
  }
}
