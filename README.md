# Cloudfactory

Cloudfactory is an open source control panel for web hosting. Each website runs in its own
rootless Podman container in a gVisor sandbox. The panel sends typed jobs to an agent on each
server. All updates are signed, and each update has a health check and an automatic rollback.

This repository holds release 0.1 (Foundation). Refer to [docs/status.md](docs/status.md) for the
items that are complete and the items that are not complete.

## Components

| Directory              | Component                                                                 |
| ---------------------- | ------------------------------------------------------------------------- |
| [`web/`](web)          | The web panel. React 19, TypeScript and Vite.                             |
| [`api/`](api)          | The panel API. Laravel 13 and PHP 8.3 or later.                           |
| [`openapi/`](openapi)  | The OpenAPI 3.1 document of the panel API.                                |
| [`agent/`](agent)      | The agent `cf-agent`. Go. It runs as root on each server.                 |
| [`cli/`](cli)          | The command line tool `cf`. Go. It uses only the public API.              |
| [`install/`](install)  | The installation script and its tests.                                    |
| [`images/`](images)    | The Containerfiles of the runtime, edge, panel and database images.       |
| [`.github/workflows/`](.github/workflows) | CI, image scans, releases and the daily image rebuild. |
| [`docs/`](docs)        | The documentation.                                                        |

## Install

Use a new server with Debian 13 or Ubuntu 26.04. Run this command as root:

```sh
curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh -s -- --domain panel.example.com
```

The script does all checks before it changes the system. To add a second server, get a join
token in the panel (**Servers > Add server**). Then run:

```sh
curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh -s -- --join TOKEN --panel https://panel.example.com
```

Refer to [docs/installation.md](docs/installation.md) for all options and for the steps to verify
the script before you run it.

## Documentation

- [Architecture](docs/architecture.md)
- [Security model](docs/security.md)
- [Installation](docs/installation.md)
- [Development](docs/development.md)
- [Releases and signing keys](docs/releasing.md)
- [Decisions about the open questions](docs/decisions.md)
- [Status of release 0.1](docs/status.md)

## Licence

The panel, the agent, the CLI and the image build files use the
[GNU Affero General Public License 3.0](LICENSE).
