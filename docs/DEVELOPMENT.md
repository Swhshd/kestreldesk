# Development

> This file has been modified for the KestrelDesk project.

This guide covers a local source checkout. The standard Docker Compose profile builds the non-LanceDB application and uses Qdrant.

## Prerequisites

- Go version required by `go.mod`
- Node.js 24 and Corepack with pnpm 10.30.2
- Docker Engine with Compose v2 for container builds
- Git

## Frontend install, build, and tests

Run from the repository root in PowerShell:

```powershell
$env:COREPACK_ENABLE_AUTO_PIN='0'
corepack pnpm@10.30.2 --dir flowgram-editor install --frozen-lockfile
corepack pnpm@10.30.2 --dir flowgram-editor build
corepack pnpm@10.30.2 --dir web install --frozen-lockfile
corepack pnpm@10.30.2 --dir web build:sdk
corepack pnpm@10.30.2 --dir web build
corepack pnpm@10.30.2 --dir web lint --ignore-pattern 'public/flowgram-editor/**'
corepack pnpm@10.30.2 --dir web typecheck
$testFiles = @(rg --files web -g '*.test.mjs')
node --test --test-reporter=tap @testFiles
```

The web export is embedded into the Go binary and the Docker image, so build it before Go checks that compile the server package.

## Go checks

```powershell
go test -p 1 -mod=readonly -tags dev ./... -count=1 -timeout=20m
go vet -mod=readonly -tags dev ./...
```

These commands use readonly module resolution and the repository's development build tag. They are checks to run locally; this guide does not claim a result for every platform.

## Docker

After preparing the required values in `.env`, run:

```powershell
docker compose config -q
docker compose up --build -d
docker compose ps
```

Compose reads the project `.env` for interpolation and forwards only the variables explicitly listed in the Compose file. `.env` files are excluded from the Docker build context.

The default profile uses MySQL and Qdrant. The optional LanceDB profiles need a Linux/amd64 build environment with CGO and matching native artifacts. A successful default build does not validate the LanceDB profile.

## Platform limitations

- Native Windows LanceDB development requires CGO, a compatible compiler, and matching native artifacts; that environment is not established as a supported path.
- Windows race-enabled tests are environment-blocked; no Windows race pass is claimed.
- The broader internal/services race run previously timed out; it is not a passing result.
- WxWork callback timing remains a known issue.
- Ticket concurrent-write semantics have not been defined as a product contract.
