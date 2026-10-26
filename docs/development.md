# Development

## Tools

| Tool         | Version          | For                        |
| ------------ | ---------------- | -------------------------- |
| Node.js      | 22 or later      | The web panel              |
| PHP          | 8.3 or 8.4       | The panel API              |
| Composer     | 2                | The panel API              |
| Go           | 1.24             | The agent and the CLI      |
| buf          | 1                | The agent protocol         |
| ShellCheck   | 0.10 or later    | The installation script    |
| Podman       | 5                | The images                 |

## Web panel

```sh
cd web
npm ci
npm run dev        # http://localhost:5173
npm test
npm run build
```

In release 0.1, the web panel uses a mock API in `web/src/api/`. The mock API has the same
shapes as the OpenAPI document. Refer to [status.md](status.md).

## Panel API

Refer to [`api/README.md`](../api/README.md).

## Agent

```sh
cd agent
go test -race ./...
go vet ./...
```

After you change `agent.proto`, make the Go code again:

```sh
buf generate
```

CI makes the code again and fails if the result is different from the files in the
repository.

The test `internal/caddy/integration_test.go` uses a real edge container. To run it:

```sh
podman run -d -p 127.0.0.1:12019:2019 -p 127.0.0.1:18080:8080 ghcr.io/itbm/edge:VERSION
go test -tags integration ./internal/caddy/
```

## CLI

```sh
cd cli
go test ./...
go run ./cmd/cf --help
```

## Installation script

```sh
shellcheck install/install.sh install/test.sh
sh install/test.sh
dash install/test.sh
```

The tests use `CF_TEST_ROOT` to give the script a fake system. They do not change your
computer.

## Images

```sh
podman build -t frankenphp-php8.4 --build-arg PHP_VERSION=8.4 images/frankenphp
```

## Continuous integration

The workflow [`ci.yml`](../.github/workflows/ci.yml) runs for each pull request. It has these
jobs:

- **web**: type check, tests and build.
- **api**: tests and Pint on PHP 8.3 and PHP 8.4.
- **agent**: buf generate check, gofmt, go vet, tests with the race detector, and builds for
  x86-64 and ARM64.
- **cli**: tests and build.
- **install**: ShellCheck and the script tests with `sh` and `dash`.

The workflow [`images.yml`](../.github/workflows/images.yml) builds each image and scans it with
Trivy when a pull request changes `images/`.
