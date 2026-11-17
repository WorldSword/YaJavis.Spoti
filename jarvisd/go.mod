module jarvisd

replace github.com/newfpv/YaJavis/hardware => ../hardware

replace github.com/newfpv/YaJavis/installer/soloist => ../installer/soloist

go 1.27.4

require (
	github.com/coder/websocket v1.8.15
	github.com/dop251/goja v0.0.0-20261110204830-f493a2b600d6
	github.com/hajimehoshi/go-mp3 v0.3.4
	github.com/newfpv/YaJavis/hardware v0.0.0
	github.com/newfpv/YaJavis/installer/soloist v0.0.0
	github.com/pion/rtp v1.10.6
	github.com/pion/webrtc/v4 v4.2.26
	github.com/thesyncim/gopus v0.2.3
	golang.org/x/crypto v0.59.0
	golang.org/x/net v0.62.0
)

require (
	github.com/dlclark/regexp2/v2 v2.11.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pion/datachannel v1.6.3 // indirect
	github.com/pion/dtls/v3 v3.1.15 // indirect
	github.com/pion/ice/v4 v4.4.8 // indirect
	github.com/pion/interceptor v0.1.51 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.2.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.20 // indirect
	github.com/pion/sctp v1.12.0 // indirect
	github.com/pion/sdp/v3 v3.0.20 // indirect
	github.com/pion/srtp/v3 v3.1.3 // indirect
	github.com/pion/stun/v4 v4.0.1 // indirect
	github.com/pion/transport/v5 v5.1.4 // indirect
	github.com/pion/turn/v5 v5.1.3 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)
