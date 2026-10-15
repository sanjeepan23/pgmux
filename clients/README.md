# Client compatibility suites

Each folder runs the same scenarios against pgmux with real drivers:

| Scenario | What it checks |
| --- | --- |
| `basic_crud` | create table, insert, select, update, delete |
| `transaction_commit_rollback` | commit is visible, rollback is not, an error mid-transaction recovers |
| `prepared_statements` | one named statement runs 100 times across reconnects |
| `concurrency` | 200 workers run 50 transactions each over a pool of 10 |
| `cancel_query` | cancel `pg_sleep(30)`, then reuse the connection |
| `copy_in_out` | `COPY FROM STDIN` and `COPY TO STDOUT` with 100k rows |
| `large_result` | stream 1M rows |
| `listen_notify` | works in session mode, fails with a clear error in transaction mode |
| `tls` | connect with `sslmode=verify-full` |

## Run

```sh
make compose-up                          # Postgres 16 with TLS
./bin/pgmux -config clients/pgmux.yaml   # pgmux on localhost:6432
PGMUX_MODE=transaction make clients      # or PGMUX_MODE=session
```

Environment variables:

- `PGMUX_HOST` and `PGMUX_PORT`: pgmux address. The defaults are `localhost` and `6432`.
- `PGMUX_MODE`: `transaction` (database `appdb`) or `session` (database `sessiondb`).
- `PGMUX_CA`: CA file for the `tls` scenario. The default is `deploy/certs/ca.crt`.
