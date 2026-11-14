# Cloudfactory panel API

This is the API of the Cloudfactory panel. It uses Laravel 13 and PHP 8.4 or later.

The web panel and the `cf` command line tool use only this API. The document
[`../openapi/openapi.yaml`](../openapi/openapi.yaml) describes each route. A test makes sure
that the document and the routes agree.

## How a change occurs

1. The client sends a request, for example "create a website".
2. The API checks the request and records the desired state in the database.
3. The API puts a job in the queue and sends status 202 with the job.
4. A queue worker sends the job to the agent on the correct server. The panel uses the
   Connect protocol over mutual TLS.
5. The agent applies the change. It reports each step to `/api/agent/v1/jobs/{id}/events`.
6. The API changes the state of the website, server or rollout to agree with the result.

## Run the API for development

```sh
composer install
cp .env.example .env
php artisan key:generate
touch database/database.sqlite
```

### With no server (demonstration mode)

Set `CF_DEMO=true` in `.env`. Then start the HTTP server and the queue worker with one command:

```sh
php artisan demo:serve --fresh
```

In this mode, the API uses a demo agent (`app/Demo`) and no real server. The demo agent does
the steps of each job and reports the result, so each change completes. `--fresh` deletes all
data and makes the sample data (`database/seeders/DemoSeeder.php`).

The sample data has these users. The password of each user is `password`.

| Email                  | Role          |
| ---------------------- | ------------- |
| `admin@example.com`    | Administrator |
| `reseller@example.com` | Reseller      |
| `owner@example.com`    | Owner         |

| Command                     | Function                                                          |
| --------------------------- | ----------------------------------------------------------------- |
| `php artisan demo:serve`    | Starts the API and the demo agent. Makes the sample data if the database is empty. |
| `php artisan demo:reset`    | Deletes all data and makes the sample data. Stop the queue worker first. |
| `php artisan demo:fixtures` | Writes the API answers that the tests of the web panel use (`web/src/test/fixtures`). |

The panel ignores `CF_DEMO` when `APP_ENV` is `production`. Do not use the sample data on a
production panel.

### With a real agent

```sh
php artisan migrate
php artisan serve
```

In a second terminal, start the queue worker:

```sh
php artisan queue:work
```

The development setup uses one SQLite file. A server uses PostgreSQL, because the containers
of the panel cannot share one SQLite file. Refer to
[`../docs/decisions.md`](../docs/decisions.md).

To let a local agent report to `php artisan serve` with no TLS, set
`CF_AGENT_DIRECT_ADDRESSES=127.0.0.1` in `.env`. Do not set it on a server.

## Run the tests

```sh
php artisan test
./vendor/bin/pint --test
```

The tests use SQLite in memory. To run them with PostgreSQL also, as the CI does, give the
database in the environment:

```sh
DB_CONNECTION=pgsql DB_HOST=127.0.0.1 DB_DATABASE=cloudfactory_test \
  DB_USERNAME=cloudfactory DB_PASSWORD=secret php artisan test
```

## Settings

Set these values in `.env`:

| Name                  | Default                    | Description                                            |
| --------------------- | -------------------------- | ------------------------------------------------------ |
| `CF_AGENT_CLIENT_CERT` | `storage/pki/panel.crt`   | The client certificate that the panel sends to agents. |
| `CF_AGENT_CLIENT_KEY`  | `storage/pki/panel.key`   | The private key of the client certificate.             |
| `CF_AGENT_CERTIFICATES` | `storage/pki/agents`     | The panel keeps the certificate of each agent here. It trusts only that certificate for the server. |
| `CF_AGENT_LATEST`      | `0.1.0`                   | The latest agent version.                              |
| `CF_AGENT_TIMEOUT`     | `10`                      | The time limit in seconds for a request to an agent.   |
| `CF_AGENT_CONNECT_TIMEOUT` | `3`                   | The time limit in seconds to connect to an agent.      |
| `CF_REGISTRY`          | `ghcr.io/itbm`            | The registry of the runtime images.                    |
| `CF_PANEL_DOMAIN`      | `panel.cloudfactory.io`   | The domain of the panel.                               |
| `CF_BACKUP_TARGET`     | (empty)                   | The object storage for backups, for example `s3:host/bucket/path`. Each server uses a repository below this address. |
| `CF_MANUAL_BACKUPS`    | `10`                      | The number of manual backups that the panel keeps for each website. |
| `CF_DELETED_WEBSITE_BACKUP_DAYS` | `30`            | The number of days that the backups of a deleted website stay in the repository. |
| `CF_DOMAINS_PER_WEBSITE` | `50`                    | The maximum number of domains of one website.          |
| `CF_DEMO`              | `false`                   | The demonstration mode: a demo agent completes each job, with no server. For development only. The panel ignores it when `APP_ENV` is `production`. |
| `CF_DEMO_STEP_MS`      | `700`                     | The time of each job step of the demo agent, in milliseconds. |

The install script makes the certificates and writes these values.

## Commands

The scheduler runs the first four commands. An administrator runs the others on the panel
server (refer to [`../docs/installation.md`](../docs/installation.md#users)).

| Command | Function |
| ------- | -------- |
| `php artisan cloudfactory:backups` | Start the nightly backups at the policy time. On Sunday, start the weekly removal of old backup data on each server. |
| `php artisan cloudfactory:check-jobs` | Set silent servers to "offline". Send old jobs again, or set them to "failed". |
| `php artisan cloudfactory:check-images` | Look for new signed image tags in the registry. |
| `php artisan cloudfactory:host-updates` | Install the host updates in the maintenance window of each server. |
| `php artisan cloudfactory:install EMAIL` | Prepare a new panel: the first administrator with a temporary password, the standard images and plans, and a join token. |
| `php artisan cloudfactory:reset-password EMAIL [--remove-totp]` | Give a user a new temporary password. |
| `php artisan cloudfactory:unlock EMAIL [--address=IP]` | Remove the sign-in lock of an account. |
