# Private networks: a guide for customers

This guide is for a customer of a Cloudfactory panel. It tells you how your websites connect
to your database servers and to your other websites, and how to keep some of them apart.
Your provider can have a different name for the panel.

## What a private network is

A private network connects your websites and your database servers. They can be on one
server or on many servers. The traffic between two servers goes through an encrypted
tunnel. Nothing in a private network is open to the internet, and the websites of other
customers cannot connect to it.

Each customer has the network `default`. Each new website and each new database server is a
member of it. Thus, with no change, each of your websites can connect to each of your
database servers and to each of your other websites.

## Connect an app to a database server

Each database server has a network name: its name and `.internal`. The port is the usual
port of the engine.

| Engine     | Example of a network name | Port |
| ---------- | ------------------------- | ---- |
| MariaDB    | `shop-db.internal`        | 3306 |
| PostgreSQL | `orders.internal`         | 5432 |
| Valkey     | `sessions.internal`       | 6379 |

To find the name:

1. Open **Databases**.
2. In the list of the database servers, read the first name in the **Host** column.

Use this name and this port in the configuration of your app. For example, for a Laravel app:

```sh
DB_HOST=shop-db.internal
DB_PORT=3306
```

The name and the port are the same on each server. Your app keeps the same configuration
when your provider moves the website or the database server to a different server.

The **Host** column also shows a second name, for example `mariadb.internal:3307`. This is
the engine name. It works only for a website on the same server as the database server. Use
the network name in your apps. Your provider can move a database server, and then an app that
uses the engine name loses the connection.

## Connect a website to a different website

A website has a network name too: its Unix user with `-` in place of `_`, and `.internal`.
For example, the website with the Unix user `shop_a1b2` has the name `shop-a1b2.internal`.

- The website answers on port 80, with HTTP: `http://shop-a1b2.internal/`.
- The connection does not go through the internet. Between two servers, the tunnel encrypts
  it.
- The **Settings** tab of the website shows its network name.

A **private website** has no domain. It answers only in its networks, for example an
internal API. A **worker** runs a command, for example a queue worker, and serves no HTTP. A
worker can connect to the other members, but the other members cannot connect to it.

## Keep websites and database servers apart

Your plan can let you make named networks, for example `backend`. A website connects to a
member only when one network has the two.

To keep a shop and its database server apart from your other websites:

1. Open **Private networks**, and select **New network**.
2. Type the name, for example `backend`.
3. Select the shop and its database server, and select **Create network**.
4. Open the menu of the network `default`, and select **Change members**.
5. Remove the shop and its database server from `default`, and select **Save network**.

Now only the members of `backend` can connect to the database server of the shop. The change
applies immediately, and no website restarts.

The card on the page **Private networks** shows how many named networks your plan allows.
If the plan allows none, ask your provider for a different plan.

Notes:

- A website or a database server can be in more than one network.
- When you remove a named network, its members stay. They keep the connections of their other
  networks.
- A website with a database in a database server on a different server must stay in a
  network with that database server. The panel refuses a change that removes this
  connection.
- The engine names (for example `mariadb.internal`) do not use the networks. A website can
  always connect to your database servers on its own server with these names.

## When your provider moves a database server

Your provider can move a database server to a different server. The database server keeps
its name, its databases, its users, its passwords and its network name.

- The status of the database server is **Moving** during the move. The database server does
  not operate during this time, so your apps cannot connect to it.
- After the move, an app that uses the network name connects again with no change.
- An app that uses the engine name, for example `mariadb.internal`, can lose the connection.
  Change it to the network name.
- You cannot change the database server, or its databases and users, during the move.

If the move fails, the database server stays on its server and operates there.

## With the CLI

```sh
cf networks list
cf networks create backend --website shop.example.com --database-server shop-db
cf networks remove default --website shop.example.com --database-server shop-db
cf networks get backend
cf database-servers list
```
