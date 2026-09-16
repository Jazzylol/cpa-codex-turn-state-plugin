Configure a fixed `X-Codex-Turn-State` value for individually selected Codex credentials through a Chinese management page. Other credentials retain their existing behavior.

## Install with CPA

Add this URL to `plugins.store-sources`, refresh the CPA plugin store, and install **Codex Turn State**:

```text
https://raw.githubusercontent.com/jinshenganyuci/cpa-codex-turn-state-plugin/main/registry.json
```

The six archives use the CPA plugin-store naming convention and are covered by `checksums.txt`. Linux builds require GLIBC 2.34 or newer; a plugin-enabled CPA v7.3.4 or newer is required.

## Supported behavior

- HTTP Responses, streaming/nonstreaming, Chat Completions, and compaction.
- WebSocket handshakes; reconnect clients after changing rules.
- Per-credential retry isolation, independent enable/disable controls, and persistent rules.
- Credential tokens are not returned to the browser or rewritten by the plugin.

## Limits

OAuth refresh, quota queries, and management APICall are not modified. Current CPA image routes ignore plugin-modified headers, so an image request selecting an enabled rule is stopped before upstream with HTTP 501. Disable that credential's rule to use image routes. Native headers, model overrides, or later plugins can take precedence; do not configure the same header in multiple places.

Validated locally with a real CPA v7.3.4.1 host and a local mock upstream: 15 integration checks, 13 browser checks, Go unit/race tests, vet, and native-library compilation. No real provider credentials were used in these tests. CI builds and tests the release platforms; Windows ARM64 is cross-compiled.
