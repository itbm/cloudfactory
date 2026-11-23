# MySQL (example database pack)

This directory is an example of a database [pack](../../../docs/packs.md): a database engine
with no new agent code. It is the test case for the contracts `cf-pack/2` and
`cf-database/1`. The release does not publish this pack. Build it, sign it and install it as a
publisher does.

| File | Purpose |
| --- | --- |
| `pack.json` | The manifest. The shared test cases in `schema/pack-vectors.json` use the golang pack. |
| `Containerfile` | The image. It obeys the database contract `cf-database/1`. |
| `cf-start` | The start program. It gives the password of the administrator to the base image. |
| `cloudfactory.cnf` | The settings of MySQL for a read-only root file system. |
| `manage/` | The program `/cf/manage`, in Go, with its unit tests. |
| `test.sh` | The contract test. It runs each operation against a real MySQL server. |

The digest in `pack.json` is an example. Before you install the pack, build the image, then
write its real digest in the manifest.

## The database contract

The agent starts a database server of the pack as it starts a built-in database server:

- The container runs in gVisor as the user 999, with a read-only root file system and no
  capabilities. Only `/tmp` and the data directory `/var/lib/mysql` are writable.
- The environment has the password of the administrator in `CF_SUPERUSER_PASSWORD`.
  `cf-start` gives it to the entrypoint of the base image as `MYSQL_ROOT_PASSWORD`. The user
  `root` can connect only through the socket in the container.
- The engine listens on port 3306. The websites of the customer connect to the network name of
  the database server, for example `mysql-1.internal:3306`.
- The agent runs `/cf/manage` for each task: it makes databases and users, gives the sizes,
  and makes and loads dumps.

Each website user is `'user'@'%'`. The host firewall lets only the websites of the customer
connect. A grant has a backslash before each `_`, because `_` in a grant matches each
character.

## Test the image

1. Build the image:

   ```sh
   podman build --build-arg MYSQL_VERSION=8.4 -t cf-mysql:test -f plugins/packs/mysql/Containerfile plugins/packs/mysql
   ```

2. Run the contract test:

   ```sh
   CONTAINER=podman sh plugins/packs/mysql/test.sh cf-mysql:test
   ```

The workflow [database-pack.yml](../../../.github/workflows/database-pack.yml) runs these
steps for each pull request that changes this directory.

## Make and install the pack

1. In `pack.json`, change the registry in each `ref` to your registry. This example uses
   `ghcr.io/example`.
2. Build and push an image for each version, for example:

   ```sh
   podman build --build-arg MYSQL_VERSION=8.4 -t ghcr.io/example/cf-mysql:8.4 -f plugins/packs/mysql/Containerfile plugins/packs/mysql
   podman push --digestfile digest ghcr.io/example/cf-mysql:8.4
   ```

3. Sign each image, and write its digest in `pack.json`:

   ```sh
   cosign sign --yes --key cosign.key ghcr.io/example/cf-mysql@$(cat digest)
   ```

4. Sign the manifest:

   ```sh
   cosign sign-blob --yes --key cosign.key --tlog-upload=false --output-signature pack.json.sig pack.json
   ```

5. In the panel, add `cosign.pub` on **Packs > Trusted keys**.
6. Select **Install pack**, then select `pack.json` and `pack.json.sig`.

Customers can then select MySQL for a new database server. The panel makes these database
servers only on the servers that loaded the pack. Refer to
[packs.md](../../../docs/packs.md) for each step.

## Limits of cf-database/1

A database server of a pack engine keeps its version. It cannot move to a different server,
and it has no Adminer and no access from outside the server. To use a newer version, make a
new database server with that version. Then move the data to it, for example with `mysqldump`
in the SSH terminal of the website.
