# Contributing to Tailbus

Thanks for your interest in contributing! Here's how to get started.

## Dev Setup

**Prerequisites:**

- Go 1.25+
- `protoc` with `protoc-gen-go` and `protoc-gen-go-grpc`
- Python 3.10+ (for the Python SDK)

**Build & test:**

```bash
make build        # compile all binaries to bin/
make test         # run unit tests with race detector
make test-all     # includes integration tests
make lint         # run golangci-lint
```

## Code Style

- Format Go code with `gofmt`
- Use `slog` for structured logging (no `log` or `fmt.Println`)
- Generated protobuf code lives in `api/` — do not edit by hand

## PR Process

1. Fork the repo and create a feature branch
2. Make your changes
3. Run `make test` and `make lint` — both must pass
4. Open a PR against `main`
5. Fill out the PR template

## Python SDK

```bash
cd sdk/python
pip install -e ".[test]"
pytest
```

## Questions?

Open a discussion or issue on GitHub.
