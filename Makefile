PLUGIN := codex-turn-state

.PHONY: fmt test build package
fmt:
	gofmt -w .
test:
	go test -race ./internal/...
	go vet ./...
build:
	mkdir -p dist
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o dist/$(PLUGIN).so ./cmd/$(PLUGIN)
package: build
	python3 scripts/package.py
