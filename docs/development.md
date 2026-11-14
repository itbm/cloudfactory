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

The web panel has no sample data of its own. It always uses the panel API at `/api/v1`
(`web/src/api/live.ts`). For development, demonstrations and screenshots, use the
demonstration mode of the API. In this mode, the API has sample data and a demo agent, so each
job completes with no server.

1. In `api/.env`, set `CF_DEMO=true`.
2. Start the API and the demo agent:

   ```sh
   cd api
   php artisan demo:serve --fresh        # http://127.0.0.1:8000
   ```

   `--fresh` deletes all data and makes the sample data again.

3. Start the web panel:

   ```sh
   cd web
   npm run dev                           # http://localhost:5173, sends /api to port 8000
   ```

Sign in as `admin@example.com`, `reseller@example.com` or `owner@example.com`. The password
is `password`. The sample users have no TOTP.

The sample data (`api/database/seeders/DemoSeeder.php`) has an example of each state that a
page can show: a website that the panel could not make, a server with updates, a degraded
server, a suspended customer, a domain that waits for its DNS record.

The demo agent (`api/app/Demo`) fails these jobs, so that you can see a failure:

- a new website or a new domain with a name that starts with `fail.`;
- a new database with a name that ends with `_fail`.

The panel ignores `CF_DEMO` when `APP_ENV` is `production`. Do not set it on a server.

### Consoles for each role

The web panel has one console for each role. `web/src/lib/roles.ts` has the menu, the first
page and the permitted pages of each role.

| Role | First page | Pages of the role only |
| --- | --- | --- |
| Administrator | Overview (`/overview`) | Overview, Finish setup, Servers, Images and updates, Customers, Plans |
| Reseller | Customers (`/customers`) | Customers, Plans, My account |
| Website owner | My websites (`/websites`) | My account |

All roles have Websites, Databases, Backups, Apps, Jobs and Settings. When a user opens a page
of a different role, the panel shows the first page of the console of the user. This is not
the security boundary: the API refuses the requests that a role must not make.

When you add a page:

1. Add its route in `web/src/App.tsx`.
2. If only some roles can open it, add it to `ONLY` in `web/src/lib/roles.ts`.
3. Add it to the menu of each role that can open it, in `NAVIGATION`.
4. Do not show a text for administrators to an owner. The test `consoles.test.tsx` examines
   the pages of an owner for such words.

### Errors of jobs

A failed job shows a short text and a reference to a customer. Only an administrator gets the
error of the server (`web/src/lib/jobFailure.ts`). The API still sends the full error to all
roles, so the web panel hides it. Do not rely on this to keep a secret.

### Tests of the web panel

The tests use the answers of the real API for the sample data. The files are in
`web/src/test/fixtures`, one for each role. `web/src/test/panelApi.ts` puts them in the place
of `fetch`.

After you change an answer of the API or the demo seeder, write the files again:

```sh
cd api
php artisan demo:fixtures
```

The API test `DemoTest` fails when the files do not agree with the API.

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

The test `internal/actions/valkey_server_test.go` uses a real Valkey server. It makes Valkey
databases with the code of the agent, and checks that the user of a database cannot use a
different database or the commands of an administrator. The image workflow runs it for the
Valkey image. To run it, start Valkey 9.1.2 or later with the user file and the options of
the workflow (`.github/workflows/images.yml`), then give the address and the password of the
user `default`:

```sh
CF_TEST_VALKEY=127.0.0.1:16379 CF_TEST_VALKEY_PASSWORD=smoke-password \
  go test ./internal/actions -run '^TestValkeyServer$' -count=1 -v
```

Without `CF_TEST_VALKEY`, the test does not run. The other Valkey tests use a small Valkey
server in the test code.

The test `TestAddDatabaseRules` adds the firewall rules of the database containers to the
firewall file of release 0.1.9 (`internal/actions/testdata/firewall-0.1.9.nft`). When the
test runs as root on a computer with `nft`, nftables also checks the result. It does not
change the firewall of the computer.

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
