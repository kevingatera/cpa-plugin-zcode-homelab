# ZCode OAuth plugin for CLIProxyAPI

This homelab fork of [rensumo/cpa-plugin-zcode](https://github.com/rensumo/cpa-plugin-zcode) connects an authenticated ZCode account to CLIProxyAPI. It adds the executor methods missing from the original plugin, native browser OAuth initialization and polling, and quota reporting for the ZCode Flash grant.

OAuth accounts use `https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages`. This is the native Start Plan endpoint, separate from ordinary Coding Plan API-key traffic. Account models appear as `zcode/GLM-5.3-Flash`, so other providers keep their existing GLM routes. The plugin never forwards a consumer's gateway key to an upstream service.

## Install and configure

Use the fork's tagged GitHub release. CI builds the Linux amd64 shared library in `golang:1.26-bookworm`, matching the homelab's Debian container. Download the release ZIP and verify it against `checksums.txt` before installing `zcode.so` in the mounted plugin directory. Preserve the existing plugin and host config before an update.

Add this entry under `plugins.configs` without rewriting the rest of the host YAML:

```yaml
zcode:
  enabled: true
  dynamic_models: false
  mimic:
    enabled: true
    app_version: "3.14.4"
    platform: "linux-x64"
    os_category: "linux"
    language: "en-US"
    timezone: "America/Toronto"
    title: "Z Code@cli"
```

Start login through authenticated `GET /v8/management/oauth/auth-url?provider=zcode`, open the returned URL, and poll `/v8/management/oauth/status?state=...`. Cancel abandoned sessions with `DELETE /v8/management/oauth/session?state=...`. The host persists the completed account in its private auth directory. The native flow issues a persistent ZCode JWT without a refresh token or expiry claim; if the server revokes it, repeat browser login. The plugin preserves this credential during host refresh rather than inventing a refresh protocol.

To reuse the workstation's existing desktop login, run `python3 tools/import-local-account.py` on that workstation. It decrypts the existing local credential in memory, obtains the management key from libsecret, and uploads the account directly to the host's credential store. It does not change desktop settings or copy plaintext credentials to a local file. The tool requires Python `cryptography` and the existing libsecret management key.

For an ordinary Coding Plan API key, configure `api_key` and `base_url` instead. Static models are registered only when that key is configured. OAuth accounts always use the separate Start Plan endpoint.

## Check operation

List `/v8/management/plugins` and `/v8/management/credentials`, then check `/v1/models` for `zcode/GLM-5.3-Flash`. Test both a non-streaming and streaming prompt. Quota is available through authenticated `POST /v8/management/plugins/zcode/quota` with `{"auth_index":"..."}`. Compare available tokens before and after a request; concurrent desktop traffic can also consume the grant.

The quota endpoint reports the provider's available tokens and grant expiry. It does not assume daily renewal. An advertised 100M allocation can be returned as a `one_time` grant with a fixed expiry; claiming or renewing an offer belongs to the native ZCode account flow. Quota inspection never claims an offer or solves its CAPTCHA.

All execution uses the host's HTTP callbacks, preserving cancellation, transport policy and the homelab's redacted request archive. No new inference timeouts are introduced. No external request or credential is needed for the unit suite:

```sh
go test -race ./...
make vet
make build
```

MIT License. The original plugin and fingerprint implementation are credited to rensumo. Native C ABI integration uses CLIProxyAPI v8.0.13.
