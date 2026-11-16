# Third-party components

YaJavis uses the dependencies pinned in `jarvisd/go.mod`, `jarvisd/go.sum` and `installer/go.mod`. Their licenses apply separately from YaJavis's MIT license. Corresponding upstream source versions are identified by these files.

The panel's visual design was inspired by **dimap's [yapanel](https://github.com/dimap/yapanel-web)** (MIT, copyright 2026 dimap). The attribution remains in `jarvisd/ui/app.css`; its license is preserved in `LICENSES/yapanel-LICENSE`. See [CREDITS.md](CREDITS.md) for the authors and technical references used throughout the project.

- Lucide icons — ISC, https://github.com/lucide-icons/lucide; license preserved in `LICENSES/Lucide-LICENSE`.

- Xray-core — MPL-2.0, https://github.com/XTLS/Xray-core. Used without local modifications; source is available at the version pinned in `go.mod`.
- goja — MIT, https://github.com/dop251/goja.
- Pion WebRTC — MIT, https://github.com/pion/webrtc.
- coder/websocket — ISC, https://github.com/coder/websocket.
- gopus — see upstream license, https://github.com/thesyncim/gopus.
- Go and golang.org/x packages — BSD-style licenses, https://go.dev/LICENSE.
- github.com/hajimehoshi/go-mp3 — Apache-2.0, MP3 decoder for the wake chime (the chime sound itself is read from the speaker firmware, not bundled).
- Manrope font — SIL Open Font License 1.1; upstream distribution https://github.com/google/fonts/tree/main/ofl/manrope.
- openWakeWord — Apache-2.0, https://github.com/dscripka/openWakeWord. `models/oww.bin` is a converted feature extractor; its upstream feature models originate from Google's speech embedding work. It is not a bundled openWakeWord wake-word classifier. The custom Jarvis classifier weights are `models/wake.bin` and `models/wake_oww.bin`; raw training recordings are excluded.
- YouTube player processing follows the published approach of yt-dlp/ejs (Unlicense), https://github.com/yt-dlp/ejs.

The standard public OAuth identifiers used by upstream clients identify applications; they are not the author's account tokens or personal API keys. Each user must authenticate separately.

License texts for bundled fonts, model components and Go dependencies are collected under `LICENSES/`. Release archives include these notices alongside the binaries.
