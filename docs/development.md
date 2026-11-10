# Development

## Tools

| Tool         | Version          | For                        |
| ------------ | ---------------- | -------------------------- |
| Node.js      | 22 or later      | The web panel              |
| PHP          | 8.4 or later     | The panel API              |
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

The web panel has two modes:

| Mode | API | Used by |
| ---- | --- | ------- |
| live | The panel API at `/api/v1` (`web/src/api/live.ts`) | The production build |
| mock | An API in memory with sample data (`web/src/api/store.ts`) | `npm run dev`, the tests and the screenshots |

Set `VITE_API_MODE=live` or `VITE_API_MODE=mock` to select a mode. In live mode, the panel
hides the areas that the API of release 0.1 does not have. Refer to [status.md](status.md).

To use live mode with a local API:

```sh
cd api
php artisan migrate:fresh --seed
php artisan serve                      # http://127.0.0.1:8000

cd ../web
VITE_API_MODE=live npm run dev         # sends /api to port 8000
```

Sign in as `admin@example.com`, `reseller@example.com` or `owner@example.com`. The password
is `password`. The sample users have no TOTP, so leave the code empty.

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
shellcheck -s sh install/install.sh install/test.sh images/*/start.sh
sh install/test.sh
dash install/test.sh
```

The CI fails for each ShellCheck note, also for a note of the type "info".

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
- **api**: tests and Pint on PHP 8.4. The tests run with SQLite and with
  PostgreSQL.
- **agent**: buf generate check, gofmt, go vet, tests with the race detector, and builds for
  x86-64 and ARM64.
- **cli**: tests and build.
- **install**: ShellCheck and the script tests with `sh` and `dash`.

The workflow [`images.yml`](../.github/workflows/images.yml) builds each image and scans it with
Trivy when a pull request changes `images/`.

Dependabot ([`dependabot.yml`](../.github/dependabot.yml)) proposes updates each week for the
GitHub actions, the Composer, npm and Go packages, and the base images in `images/`.

The agent and the panel API use one list of examples for the rules of domain names and image
names: [`testdata/names.json`](../testdata/names.json). When you change a rule, change the
list. The tests of the two components then show a difference between them.
