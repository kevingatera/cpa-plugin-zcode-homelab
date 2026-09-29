# ZCode CLIProxyAPI Plugin

`zcode` is a CLIProxyAPI dynamic-library plugin that exposes the BigModel
Coding Plan upstream through CPA as a static Anthropic-format model provider.

The plugin injects ZCode desktop fingerprint headers, maps model names, and
supports streaming. It intentionally consumes Anthropic-format payloads and
lets CPA translate OpenAI/Responses/Claude client protocols through its
normal executor pipeline.

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    zcode:
      enabled: true
      priority: 1
      api_key: ""                    # empty: pass through each CPA client key
      base_url: "https://open.bigmodel.cn/api/anthropic"
      models:
        - "GLM-5.3"
        - "GLM-5.3-Flash"
      model_map: {}                  # client model -> upstream model
      timeout_seconds: 300
      mimic:
        enabled: true
        app_version: "3.14.4"
        session_id: ""               # empty: generated per plugin process
        trace_id: ""                 # empty: generated per plugin process
        user_id: ""                  # empty: stable ID derived from API key
        platform: "win32-x64"
        os_category: "windows"
        os_version: "10.0.19045"
        language: "zh-CN"
        timezone: "Asia/Shanghai"
        release_channel: "production"
        title: "Z Code@electron"
```

`api_key` can be used for a fixed upstream credential. When omitted, the
request client's `Authorization: Bearer ...` or `x-api-key` header is used
for the upstream call.

## Build

```bash
make test
make vet
make build
make package
```

On Windows this creates `dist/zcode.dll`; on macOS, `dist/zcode.dylib`; on
Linux/FreeBSD, `dist/zcode.so`.

Install locally by copying the dynamic library to CPA's platform plugin
directory, for example:

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/windows/amd64
cp dist/zcode.dll /path/to/CLIProxyAPI/plugins/windows/amd64/zcode.dll
```

## Release assets

Each GitHub release includes:

```text
zcode_<version>_<goos>_<goarch>.zip
checksums.txt
```

Each zip contains the shared library at its root.

## Compatibility

This plugin implements the CPA v8 C ABI and uses CLIProxyAPI v8.0.4. It
registers static models and an executor with `anthropic` input/output format;
CPA performs protocol translation for other client formats.
