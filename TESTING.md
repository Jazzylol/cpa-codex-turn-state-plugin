# Validation

The integration suite loads the compiled C ABI plugin into the unmodified
`jinshenganyuci/cli-proxy-api:codex-identity-v7.3.4.1` Docker image. Two synthetic
Codex OAuth files use a local CONNECT proxy and a test TLS authority. The fixture
only accepts `chatgpt.com:443` CONNECT requests and serves them locally; it never
forwards requests to the real provider. Real credentials are not used.

Verified behavior:

- Management API authentication and a public static page with no credential data.
- Default inactivity and preservation of client headers for unconfigured credentials.
- Literal header replacement on HTTP streaming, HTTP nonstreaming, Chat Completions,
  and Responses compaction.
- WebSocket handshake override and unconfigured-credential isolation.
- Independent rules for two credentials.
- An injected upstream 502 selects another credential without leaking the first
  credential's override into the retry; the original client value is restored.
- Disabling and deleting a rule restores the existing client behavior.
- Image requests selecting a configured credential stop with HTTP 501 before any
  upstream request because this host's image executor ignores plugin headers.
- Browser login, credential selection, literal-value persistence, input validation,
  disabling, deletion, logout, mobile width, and absence of browser storage writes.
- Unit and race tests cover saved-state reload, stale credential indices, concurrent
  reads and saves, malformed state, header injection, native-header conflicts,
  minimal credential projection, and failed-save atomicity.

Run ordinary checks:

```sh
gofmt -w .
go test -race ./internal/...
go vet ./...
make build
```

Run the Docker suite with an optional locally installed Playwright module:

```sh
CPA_PLUGIN_INTEGRATION=1 \
CPA_BROWSER_MODULE=/absolute/path/to/node_modules/playwright \
CPA_BROWSER_EXECUTABLE=/absolute/path/to/chromium \
go test -v ./tests/integration -count=1
```

The suite creates temporary loopback listeners and a temporary container, removes
the container on exit, and writes captures under `validation/`. The image tag can
be overridden with `CPA_TEST_IMAGE`. The tests do not establish that OpenAI will
accept an arbitrary manually configured turn state. OAuth refresh, quota queries,
management APICall, existing WebSocket connections, and image support are outside
the override contract described in `README_CN.md`.
