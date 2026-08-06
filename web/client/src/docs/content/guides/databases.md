# Managed Databases

**Platform → Databases** runs a database for you: provisioned, monitored, backed up and upgraded by the platform, reachable from your own network on a private endpoint. You do not get a server to log into — you get a connection string.

## Engines

| Engine | Notes |
|---|---|
| PostgreSQL | The default choice. Also the backend FerretDB runs on. |
| MySQL | |
| MariaDB | |
| Valkey | **Beta.** Redis-compatible key-value store. |
| FerretDB | MongoDB-compatible wire protocol over PostgreSQL. |
| OpenSearch | Search and analytics, HTTPS on :9200. Ships with Dashboards. |
| Kafka | Event streaming, SASL on :9094. |

The engine and its major version are chosen at creation and cannot be swapped later — a different engine is a different database. Minor and major *version* upgrades are supported in place (see below).

## Creating a database

![The Create database dialog](/docs-img/db-create.png)

**Name** is display-only, like everywhere else in the console.

**Version and replicas.** Replicas beyond one give you a replicated cluster; the shape depends on the engine. One replica is a single node with no redundancy — fine for development, not for anything you would miss.

**Size** picks CPU, memory and disk. Disk is a block volume and is billed as one.

**Storage class** defaults to the cluster default, which is what you want unless the operator has published something special.

**Network and subnet** decide where the endpoint lands. The database is placed on an internal load balancer inside *your* network, so only things on that network — your servers, your Kubernetes pods — can reach it. There is no public endpoint, by design.

Creation takes a few minutes. Most of that is the internal load balancer being programmed; the console tells you so while it waits.

## Connecting

The **Connection** tab carries everything a client needs.

![The Connection tab, with the endpoint and access controls](/docs-img/db-connect.png)

**Endpoint** is a DNS name, not a bare address — use it rather than resolving it once and hard-coding the result, because the address behind it can change. A replicated instance also publishes a separate **read-only** endpoint that balances across replicas; on a single-instance database it is shown as not enabled, which is expected rather than a fault.

**Show connection info** reveals the credentials and a ready-made connection string. **Reset password** rotates the administrator credential — existing connections are not torn down, but anything holding the old password will fail on its next connect.

**Allowed CIDRs** narrows who may reach the endpoint. It defaults to the whole network the database sits on; set it to specific ranges when only some of your servers should have access.

Because the endpoint is internal, connect from something inside that network:

```sh
# from a server on the same network
psql "postgres://<user>:<password>@<endpoint>:5432/<database>"
```

If you cannot connect, check that the client is on the database's network and inside the allowed CIDRs — that is the cause far more often than a wrong password.

**Databases & users** manages logical databases and roles inside the instance. Creating an application user here, rather than reusing the administrator credential everywhere, is worth the two minutes.

## Operating a database

![A database's Overview tab, with its size and lifecycle actions](/docs-img/db-detail.png)

From the detail page:

- **Resize** changes CPU and memory. **Resize storage** grows the disk — it only grows, never shrinks.
- **Scale replicas** adds or removes replicas.
- **Autoscale** lets the platform grow the instance on its own. Storage autoscaling watches actual disk usage, so it needs the operator's metrics stack to be available.
- **Upgrade version** moves to a newer engine version. As with Kubernetes, only upgrade paths the engine itself supports are offered.
- **Restart** restarts the instance.
- **Platform update** appears when the operator publishes a newer version of the managed components. It is opt-in, restarts the components, and leaves your engine version and data alone.

**Configuration** exposes the engine parameters the platform allows you to change. **Logs** shows the instance's own logs without needing shell access.

## Backups

The **Backups** tab covers both scheduled and on-demand backups, and restoring.

A restore creates a **new database** from a backup — it never overwrites the running one. That is deliberate: you can inspect the restored copy, then move traffic when you are satisfied, and you still have the original if you are not.

Backups already taken are kept until their retention expires, including after you delete the database they came from.

## Deleting

**Delete database** removes the instance and its storage. Backups already taken are kept until their retention expires, so a delete is not the same as destroying your history — but the live data and its volume are gone immediately.

## What this costs

A database bills for the compute size and the disk you gave it, plus the internal load balancer that fronts it, for as long as it exists. Replicas multiply the compute and disk. Stopping traffic to a database does not stop the bill — delete it, or scale it down.
