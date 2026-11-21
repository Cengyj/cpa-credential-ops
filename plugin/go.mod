module github.com/cliproxyapi-plugins/model-quality

go 1.26.10

require (
	github.com/MoeForge/moego v0.1.0-alpha.4
	github.com/google/uuid v1.6.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/router-for-me/CLIProxyAPI/v8 v8.0.0
	github.com/sirupsen/logrus v1.9.3
	go.etcd.io/bbolt v1.5.0
	golang.org/x/net v0.60.0
	golang.org/x/text v0.42.0
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/sys v0.48.0 // indirect

replace github.com/router-for-me/CLIProxyAPI/v8 => ../workspaces/CLIProxyAPI-official

replace github.com/MoeForge/moego v0.1.0-alpha.1 => github.com/router-for-me/moego v0.1.2
