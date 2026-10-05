# ZCode plugin homelab workflow

Work on `homelab`. This is a fork of rensumo/cpa-plugin-zcode. Do not open an upstream PR unless explicitly requested.

Run `gofmt`, `go test -race ./...`, `make vet`, and `make build` before release. Commit with `git -c commit.gpgsign=false`. Tag pushes build a Debian bookworm Linux amd64 release in GitHub CI. omv-108 installs the checksummed GitHub release asset, never a locally copied build. Backend changes remain in CLIProxyAPI-homelab and follow its GHCR workflow.

Preserve the existing JS Handler plugin and other consumers. Host plugin files are `/srv/ssd-apps/cliproxyapi/plugins`; credentials are in the adjacent `auths` directory. Make timestamped config backups and targeted YAML edits. Never print tokens, management keys, or raw credential files.

OAuth uses the native Start Plan endpoint and prefixed models. Do not silently fall back to ordinary Coding Plan API keys: they have separate billing. Do not claim daily renewal from a grant expiry or client header. Only the provider's billing response and live request establish usage. Do not add inference timeouts, print raw upstream errors, or forward consumer API keys to providers.
