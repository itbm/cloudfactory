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

## Compatibility while the version is 0.x

While the version of Cloudfactory is 0.x, the project is in development. Each release is a
new installation. Obey these rules:

- Do not keep backwards compatibility. A change can rename or remove a column, a setting, an
  API field, a route, a CLI command or a file format.
- Do not write migrations that move old data, shims or compatibility layers. Change the
  migration that made the table, or add a migration that makes the new form directly.
- Remove the old code in the same change. Do not keep a fallback for data of an earlier
  release.
- Change the tests, the fixtures, the OpenAPI document and the documentation in the same
  change.

There is one exception: the pack contracts `cf-pack/1` and `cf-runtime/1` are stable,
because publishers outside the project depend on them (refer to
[packs.md](packs.md#the-stable-contract)).

Release 1.0 starts the compatibility rules. The decision is in
[decisions.md](decisions.md#compatibility-while-the-version-is-0x).

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

To try passkeys, open the web panel at `http://localhost:5173`, not at `127.0.0.1`. A browser
makes a passkey only on HTTPS or on `localhost`. The passkeys of the sample data are for the
domain `panel.cloudfactory.io`, so you cannot sign in with them. Add a new passkey in
**Settings**. `CF_PASSKEY_LOCAL_ORIGINS` gives the other origins of `localhost` that the API
accepts in development.

Single sign-on also works on `localhost` in development, with HTTP. The providers of the
sample data are examples: they do not work. To try single sign-on, add a provider of your own
in **Settings > Single sign-on**, for example a Keycloak container. In development, the
addresses of the provider can use HTTP. Register the redirect URI that the panel shows, for
example `http://localhost:5173/api/v1/auth/sso/oidc/callback`.

The sample data (`api/database/seeders/DemoSeeder.php`) has an example of each state that a
page can show: a website that the panel could not make, a server with updates, a degraded
server, a suspended customer, a domain that waits for its DNS record.

The demo agent (`api/app/Demo`) fails these jobs, so that you can see a failure:

- a new website or a new domain with a name that starts with `fail.`;
- a new database with a name that ends with `_fail`;
- a deploy from a repository with `fail` in its address (the build step fails);
- a deploy from a repository with `unhealthy` in its address (the health check after the
  switch fails, and the website goes back to the previous release). With a migration
  command, the website keeps the new release, and the owner gets an alert;
- a cron job with `fail` in its command, when you select **Run now**;
- a command with `fail` in it, in **Commands** (the command ends with exit code 1);
- a change of a DNS zone with a name that starts with `fail.`;
- the download of an account import with `fail` in the name of its file or in its address.

The demo agent does not read the archive of an import. Each import gets the same sample
account (`api/app/Demo/DemoImport.php`). A file name with `directadmin`, or a file name that
starts with `user.`, gives a DirectAdmin backup. A file name with `plesk`, or a file name that
starts with `backup_`, gives a Plesk backup. A file name with `hestia`, or a file name of the
form `USER.2026-11-22_05-00-00.tar`, gives a HestiaCP backup. Each other name gives a cPanel
backup.

The demonstration mode sends no request for an uptime check. Each sample website answers with
HTTP status 200 and a page with its name. A check with `fail` in its path gets no answer, and a
check that expects a different status or a text that is not on the page fails.

In the demonstration mode, the file manager shows a small set of sample files for each
website, and keeps your changes in the cache. The logs are sample entries for the last two
days. The search and its filters work on these entries. **Run a command** gives a sample
output for some usual commands, for example `php -v`, `php artisan migrate` and
`composer install`.

The demo mode does not ask the public DNS. For a DNS zone with a name that starts with
`elsewhere.`, it answers that the domain uses the name servers of a registrar. For each other
zone, it answers that the domain uses the name servers of the panel.

The panel uses `CF_DEMO` only when `APP_ENV` is `local`, `testing`, `demo` or `development`.
Do not set it on a server.
To try **Open in Adminer** in the demonstration mode, start Adminer on your computer and set
its address in `api/.env`, for example `CF_ADMINER_URL=http://127.0.0.1:8090`. The panel adds
the path of the customer to this address:

```sh
mkdir -p /tmp/adminer && cp images/adminer/index.php /tmp/adminer/
curl -fsSL -o /tmp/adminer/adminer.php \
  https://github.com/vrana/adminer/releases/download/v5.5.1/adminer-5.5.1.php
php -S 127.0.0.1:8090 -t /tmp/adminer /tmp/adminer/index.php
```

Adminer opens only server names that end in `.internal`. To connect to a local database,
add the name of the database server to `/etc/hosts`, for example
`127.0.0.1 shop-db.internal`.


### The web terminal in development

The demo agent cannot run a shell. In the demonstration mode, the job `OpenTerminal` fails
with the message "The demonstration has no terminal relay". The sample data has one closed
terminal session with its commands and its recording, on the website `api.acme.example.com`.

To see the browser side of the terminal, do these steps:

1. Set `CF_DEMO_TERMINAL=true` in `api/.env`. The demo agent then completes the job
   `OpenTerminal`, and the session stays open.
2. Start the terminal relay with the `APP_KEY` of the API. The development server of the web
   panel sends `/api/terminal/` to port 8091 (`web/vite.config.ts`):

   ```sh
   cd agent
   APP_KEY=$(grep '^APP_KEY=' ../api/.env | cut -d= -f2-) go run ./cmd/cf-terminal-relay --listen 127.0.0.1:8091
   ```

3. Open the terminal in the web panel. The browser connects to the relay and waits for the
   agent for 1 minute.

The relay waits for an agent. No agent connects in the demonstration mode, so the terminal
closes after 1 minute. To test the agent side, use the tests of `agent/internal/actions`
(`terminal_test.go`) and `agent/internal/relay`. They connect a test relay, a fake shell and a
fake panel.

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
`web/src/test/fixtures/ROLE`, with one file for each request, for example
`owner/websites_perpage_100_page_1.json`. Each file has the request, the status and the body.
`web/src/test/panelApi.ts` puts them in the place of `fetch`. In a test, use
`fixture(role, request)` to read one answer.

To add a request, add its path to `PATHS` or `WEBSITE_PATHS` in `api/app/Demo/Fixtures.php`,
in alphabetical order.

After you change an answer of the API or the demo seeder, write the files again:

```sh
cd api
php artisan demo:fixtures
```

The API test `DemoTest` fails when the files do not agree with the API.

The command gives the same files each time. It gives the same IDs, random strings, keys and
certificates on each run. Each table has its own sequence of IDs. Thus a new row changes only
the IDs of the rows that come after it in the same table.

Sign in as `admin@example.com`, `reseller@example.com` or `owner@example.com`. The password
is `password`. The sample users have no TOTP, so leave the code empty. In the
demonstration mode, the panel does not ask the sample administrator and the sample reseller
to set up TOTP. To see the page **Set up an authenticator app**, set `CF_REQUIRE_TOTP=true`
in `api/.env`.

## Panel API

Refer to [`api/README.md`](../api/README.md).

## Agent

The Go code of the agent protocol (`agent/gen`) is not in Git. Only a `doc.go` file of each
package is in Git, so that `go mod tidy` finds the packages without the code (Dependabot runs
it). Make the code after a clone, after a pull and after a change to `agent.proto`:

```sh
cd agent
go generate
```

The command needs [buf](https://buf.build/docs/cli/installation/), for example
`go install github.com/bufbuild/buf/cmd/buf@latest`. The two protoc plugins are tools in
`go.mod`, so they have the same versions as the libraries. The command also writes the input
rules again (refer to "Input rules").

```sh
go test -race ./...
go vet ./...
```

The tests use a fake for Knot DNS. One test starts a real Knot DNS server with the
configuration and the zone files of the root helper, and asks it with `kdig`. It runs only
when Knot DNS is installed and `CF_TEST_KNOT=1` is set:

```sh
sudo apt-get install knot knot-dnsutils
CF_TEST_KNOT=1 go test ./internal/actions -run TestKnotServer
```

CI makes the Go code of the protocol before the build and the tests. It does not compare the
code with a copy, because Git has no copy.

### Root work in the agent

The worker of the agent is not root (refer to
[architecture.md](architecture.md#the-processes-of-the-agent)). When an action needs root,
use one of the three lists of the root helper. Obey these rules:

- **A command.** Add its form to the policy in `internal/actions/policy.go`. Give a root
  command a full list of fixed arguments. Do not add a form that lets the worker select a
  program, an option or a path of root.
- **An operation.** Add a name and a function to `RootOps` in `internal/actions/root.go`. The
  values are typed. Let the worker give a name (a Unix user, the ID of a database server, the
  ID of a job), not a path, and make each path from the settings of the root helper. Examine
  each value in the function, because the root helper does not trust the worker.
- **A connection.** Add the address to `allowAddress` in `internal/actions/dial.go`, and open
  the connection with `DialLocal` or `DialEdge`.
- A function with two sides starts with `if d.Gate != nil { return d.Gate.Call(...) }`. The
  code after this line runs only in the root helper.
- When root must open a file in a directory that a different user controls, use the package
  `internal/safefs`, not `os`.
- Add a refusal case to `internal/actions/boundary_test.go` for each new command, operation
  and connection.

The tests of the actions use the two sides: the test `Deps` is a worker, with a root helper
in the same process. CI runs the tests as a user that is not root. Run them also as root,
because some tests of the root helper need root:

```sh
sudo go test ./internal/actions/ ./internal/safefs/ ./cmd/cf-agent/
```

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

The test `TestAddDatabaseRules` adds the firewall rules of the database servers to the
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

## Input rules

The panel API and the agent check the same values, for example a domain name or the name of a
PHP extension. The rules for these values are in one file:
[`schema/input-rules.json`](../schema/input-rules.json). Do not write such a rule in the PHP code
or in the Go code. Refer to [`schema/README.md`](../schema/README.md) for the format of the file.

To change a rule or to add a rule:

1. Change `schema/input-rules.json`.
2. Add valid and invalid examples to
   [`schema/input-vectors.json`](../schema/input-vectors.json). Each rule must have the two
   types of example.
3. Write the generated files again:

   ```sh
   cd agent
   go run ./cmd/cf-rules
   ```

4. Run the tests of the agent and of the panel API. The two run all the examples.
5. Commit the rules file, the examples and the two generated files together.

The generator writes these files. Do not change them by hand:

- `agent/internal/validate/rules_gen.go`: one Go function for each rule, for example
  `validate.Domain`.
- `api/app/Validation/InputRules.php`: one pattern and one message for each rule. Use
  `Input::rule('domain')` in the rules of a request, or `Input::valid('domain', $value)`.

## Pack contract

The panel API (`App\Services\Packs\PackManifest`) and the agent (`agent/internal/packs`)
check pack manifests with the same rules. The test cases are in
[`schema/pack-vectors.json`](../schema/pack-vectors.json). The tests of the two components run
each case.

The contracts `cf-pack/1` and `cf-runtime/1` are stable. Do not change what a valid manifest
is. To fix a check that accepts a manifest that the contract does not permit:

1. Add an invalid case to `schema/pack-vectors.json`.
2. Change the Go code and the PHP code.
3. Run the tests of the agent and of the panel API.
4. Change [packs.md](packs.md) when the text of the contract was not clear.

A new key or a new value needs a new contract, for example `cf-pack/2`. Refer to
[packs.md](packs.md#the-stable-contract).

## Parallel pull requests

Many pull requests are open at the same time. When one merges, Git must merge the others
without a conflict. GitHub does not use merge drivers from `.gitattributes`, so the layout of
the files must prevent conflicts. Git merges two changes only when at least one line that does
not change is between them. Obey these rules:

- Do not commit generated code that the build can make. The Go code of the agent protocol is
  not in Git (refer to "Agent").
- A generator must not align its output, because a new long name then changes each line. For
  example, `cf-rules` writes one statement for each rule, not a map with aligned values.
- Give each item of data its own file when the items change separately. The web fixtures have
  one file for each request.
- Put one item on each line of a list. Put a new item in alphabetical order, or after the
  item that is nearest to it in subject. Do not add all new items at the end of a list.
- Do not write a comment that names each item of a list. Each new item then changes the
  comment.
- In a document, separate the items with a blank line. A Markdown table has no blank lines
  between its rows, so do not use a table for a list that many pull requests change, for
  example [status.md](status.md).
- Put a new area of code in a new file when the language permits it, for example the Connect
  methods of a new area of the agent in a new file in `agent/internal/server`.

To adopt a change to the layout in an open pull request, merge `main` into the branch, accept
the version of `main` for the generated files, and make them again.

## Continuous integration

The workflow [`ci.yml`](../.github/workflows/ci.yml) runs for each pull request. On a pull
request, a job runs only when the files that it tests change. For example, a change in `web/`
runs only the job **web**. A change to the workflow, and a push to `main`, run all jobs. The
workflow has these jobs:

- **web**: type check, tests and build.
- **api**: tests and Pint on PHP 8.4. The tests run with SQLite and with
  PostgreSQL.
- **agent**: buf lint, the generated code, input rules check, gofmt, go vet, tests with the race detector, and builds for
  x86-64 and ARM64.
- **cli**: tests and build.
- **install**: ShellCheck and the script tests with `sh` and `dash`.

The workflow [`images.yml`](../.github/workflows/images.yml) builds an image and scans it with
Trivy when a pull request changes the files of that image. For example, a change in `api/`
builds only the image `panel-api`. A change to the workflow builds all images. A new push to
the pull request stops the builds of the earlier push.

Dependabot ([`dependabot.yml`](../.github/dependabot.yml)) proposes updates each week for the
GitHub actions, the Composer, npm and Go packages, and the base images in `images/`.

The agent job also runs the generator of the input rules and makes sure that the generated
files are current. Refer to "Input rules".
