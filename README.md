# Codex Turn State

[中文说明](README_CN.md)

A native CLIProxyAPI plugin that overrides `X-Codex-Turn-State` for individually
selected Codex credentials. It includes a Chinese management page and leaves all
credentials unchanged until a rule is explicitly enabled.

## Install from the CPA plugin store

Add this registry URL to the existing `plugins.store-sources` list in CPA's
configuration:

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/jinshenganyuci/cpa-codex-turn-state-plugin/main/registry.json
```

Merge the snippet with existing plugin settings. Do not replace another plugin's
configuration or add a duplicate top-level `plugins` key. Refresh **Plugin Store**
in the management panel, find **Codex Turn State**, and select **Install**.

Open the plugin's **Codex Turn State** resource menu, authenticate with the CPA
management key, select a credential, paste the header value, enable the rule, and
save. The resource is also available at
`/v0/resource/plugins/codex-turn-state/panel`.

CPA automatically selects a release asset named
`codex-turn-state_<version>_<goos>_<goarch>.zip` and verifies `checksums.txt`.
Releases provide Linux, macOS, and Windows libraries for AMD64 and ARM64.
Linux requires GLIBC 2.34 or newer. Use a plugin-enabled CPA v7.3.4 or newer;
`no-plugin` distributions cannot load native plugins.

## Behavior and limits

- Rules match the actual selected credential ID and runtime index after selection.
- HTTP streaming/nonstreaming Responses, Chat Completions, compaction, and WebSocket
  handshakes support the override. Retries re-evaluate the newly selected credential.
- Other credentials retain their existing headers. Rules are stored separately
  from OAuth credential files with private file permissions.
- Header values are literal, single-line printable ASCII, up to 8192 bytes.
- Reconnect a WebSocket client after changing a rule; existing upstream handshakes
  cannot be edited.
- OAuth login/refresh, quota queries, and management APICall are outside the model
  interception interface and are not modified.
- Current CPA image routes bypass plugin-modified headers. A request selecting an
  enabled credential on those routes is rejected before upstream with HTTP 501
  and `turn_state_image_transport_unsupported`. Disable the credential's rule to
  use those image routes.
- Native credential headers, model header overrides, and later plugins can take
  precedence. Enabling a rule rejects an existing native credential-header conflict;
  do not configure this header elsewhere at the same time.

`state_file` defaults to `plugins/codex-turn-state-state.json`, relative to CPA's
working directory. Persist that directory when using Docker, or configure an
explicit path under `plugins.configs.codex-turn-state.state_file`.

## Development

```sh
go mod download
gofmt -w .
go test -race ./internal/...
go vet ./...
make package
```

See [TESTING.md](TESTING.md) for isolated tests with a real CPA Docker host and a
local mock upstream. These tests do not use real provider credentials or establish
that OpenAI accepts arbitrary manually supplied turn-state values.

The native ABI follows the [CLIProxyAPI plugin examples](https://github.com/router-for-me/CLIProxyAPI/tree/main/examples/plugin).
