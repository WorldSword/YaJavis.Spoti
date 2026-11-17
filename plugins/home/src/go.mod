module github.com/newfpv/YaJavis/plugins/home

go 1.27.4

require (
	github.com/coder/websocket v1.8.15
	github.com/newfpv/YaJavis/pluginsdk v0.0.0
	rsc.io/qr v0.2.0
)

require golang.org/x/net v0.62.0 // indirect

replace github.com/newfpv/YaJavis/pluginsdk => ../../../pluginsdk
