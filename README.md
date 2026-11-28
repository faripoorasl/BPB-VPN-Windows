# BPB VPN for Windows

**BPB VPN** for Windows is the Windows-desktop port of the BPB-VPN Android app — the same Flutter interface, the same Go core, the same protocols, adapted to Windows natively.

> **One app. Multiple ways to stay connected.**
> Highly customizable Cloudflare CDN connections, Psiphon, Tor, SSH and Serverless solutions in one VPN client powered by [Xray-core](https://xtls.github.io).

[فارسی](docs/INSTALL-FA.md) — راهنمای نصب و استفاده به فارسی در پوشه‌ی `docs/` قرار دارد.

## What is reused from the original app

| Layer | Source | Windows |
| --- | --- | --- |
| Interface (UI/UX) | `lib/` — Flutter Material 3, Baloo font, all pages | **Unchanged**; a fixed 420×760 window keeps the phone layout |
| VPN core | `core/` — Xray-core (vless/warp/masque/ssh/serverless, fragment, REALITY, uTLS, ECH) | **The same Go code**, cross-compiled; the TUN is Xray's own Windows inbound (wintun) |
| Exits | `psiphon/`, Tor expert bundle | The same `psiphon` Go module → `psiphon.exe`; Tor 16 + lyrebird for Windows |
| Worker panel | `worker/worker.js` | **Unchanged** — deployed to your Cloudflare account from the app |
| Setup wizard, DNS, routing, fragments, noises, per-app, chains, logs… | `lib/ui/`, `lib/services/` | **Unchanged** except small platform branches |

## What the Windows port adds

- **`core/cmd/bpbcore`** — the Windows service host (the Android `MainActivity`/`BpbVpnService`/`CoreManager`/`ExitProcess`/`TrafficMeter` layer, in Go): one Xray instance, the Psiphon/Tor exit processes, traffic sampling, logs, the scanner, the setup-wizard Tor and the verification loop, served to the app over a localhost JSON-lines protocol. It relaunches itself elevated (UAC) only when VPN (TUN) mode needs it; proxy mode never asks for rights.
- **`lib/services/windows/`** — the platform-channel implementation over that service: Windows file dialogs, Explorer sharing, the system HTTP proxy (`appendHttpProxy`), QR decode from an image (no camera on desktop), the installed-programs list for per-app proxying (routing rules on `process`), and the geo/CA file plumbing.
- **`windows/`** — the Flutter Windows runner: fixed-size window (no resize/maximize), single instance, BPB icon and version info.

## Feature parity

Everything the Android app does, including: Worker (VLESS over WS/TLS with best-ping across ports and clean IPs), WARP and obfuscated WARP, MASQUE (h2/h3), Tor (direct/snowflake/obfs4/meek/webtunnel/custom bridges), Psiphon, SSH exits, Serverless (fragment/UDP-noise/DNS/domain-fronting), chain proxies (v2ray links and WireGuard), TCP fragmentation, UDP noises, REAL Cloudflare/WARP/MASQUE endpoint scanners, DNS manager with DoH/DoQ tests, routing presets (bypass/ sanction bypass/ block), per-app proxy, LAN sharing with authentication, system HTTP proxy, config viewer, logs, geo-asset updates, in-app worker deployment and updates, settings export/import, themes.

See [docs/FEATURES-TEST-REPORT-FA.md](docs/FEATURES-TEST-REPORT-FA.md) for the full feature/test matrix in Persian.

## Download & install

Grab `BPB-VPN-Setup-x.y.z-windows-x64.exe` (installer) or `bpb-x.y.z-windows-x64.zip` (portable) from the [Releases](https://github.com/faripoorasl/BPB-VPN-Windows/releases) page. Windows 10/11 x64. VPN mode asks for administrator rights once per connection (the TUN device); Proxy mode doesn't.

## Building from source

```bash
# Tests (Linux/macOS/Windows): the Go suites + the Flutter suite + the core integration test
./scripts/test_core.sh

# The Windows build (what the CI does, see .github/workflows/windows.yml)
GOOS=windows go build -o build/bpbcore.exe -C core ./cmd/bpbcore
GOOS=windows CGO_ENABLED=0 go build -o build/psiphon.exe -C psiphon .
flutter build windows --release   # + the bundled assets the CI fetches
```

## License

GNU GPL v3.0 — see [LICENSE](LICENSE). BPB VPN for Windows is Copyright (C) 2026 Fariborz (faripoorasl) and is published from its own repository, [faripoorasl/BPB-VPN-Windows](https://github.com/faripoorasl/BPB-VPN-Windows), as a derivative work of the BPB-VPN Android app under the same license; credit for the original design and logic goes to the BPB community.
