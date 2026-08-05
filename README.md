# pgmux

pgmux is a Postgres connection pooler in Go. It speaks the Postgres wire protocol, so thousands of
client connections can share a small pool of real server connections.

- Session and transaction pooling, for Postgres 14 to 17.
- SCRAM-SHA-256 and MD5 authentication, with a userlist file or an `auth_query`.
- TLS from clients and to Postgres, including `verify-full`.
- Prepared statements in transaction mode, with no client changes.
- Prometheus metrics, an admin HTTP API, a live dashboard, and a Grafana dashboard.
- Config hot reload, graceful shutdown, and cancel request support.

![pgmux dashboard during a load test](docs/dashboard.png)

## Quick start

You need Go 1.26, Docker and `psql`.

```sh
make compose-up        # Postgres 16 with TLS and seed data
make run               # pgmux on 127.0.0.1:6432, admin API on 127.0.0.1:9930
PGPASSWORD=app psql "host=127.0.0.1 port=6432 user=app dbname=appdb" -c "select count(*) from items"
```

Open http://127.0.0.1:9930/ for the dashboard after `make dashboard`.

To run pgmux in Docker as well:

```sh
docker compose -f deploy/docker-compose.yml --profile pgmux up -d --build
```

