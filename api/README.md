# Cloudfactory panel API

This is the API of the Cloudfactory panel. It uses Laravel 13 and PHP 8.3 or later.

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
php artisan migrate --seed
php artisan serve
```

In a second terminal, start the queue worker:

```sh
php artisan queue:work
```

The seed data has three users. The password of each user is `password`.

| Email                  | Role          |
| ---------------------- | ------------- |
| `admin@example.com`    | Administrator |
| `reseller@example.com` | Reseller      |
| `owner@example.com`    | Owner         |

Do not use the seed data on a production panel.

## Run the tests

```sh
php artisan test
./vendor/bin/pint --test
```

## Settings

Set these values in `.env`:

| Name                  | Default                    | Description                                            |
| --------------------- | -------------------------- | ------------------------------------------------------ |
| `CF_AGENT_CLIENT_CERT` | `storage/pki/panel.crt`   | The client certificate that the panel sends to agents. |
| `CF_AGENT_CLIENT_KEY`  | `storage/pki/panel.key`   | The private key of the client certificate.             |
| `CF_AGENT_CERTIFICATES` | `storage/pki/agents`     | The panel keeps the certificate of each agent here. It trusts only that certificate for the server. |
| `CF_AGENT_LATEST`      | `0.1.0`                   | The latest agent version.                              |
| `CF_REGISTRY`          | `ghcr.io/cloudfactory`    | The registry of the runtime images.                    |
| `CF_PANEL_DOMAIN`      | `panel.cloudfactory.io`   | The domain of the panel.                               |
| `CF_BACKUP_TARGET`     | (empty)                   | The object storage for backups, for example `s3://bucket/path`. |

The install script makes the certificates and writes these values.
