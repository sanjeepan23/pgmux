# Benchmarks

`run.sh` compares three setups on the same machine with pgbench 17:

1. Direct: clients connect to Postgres (`max_connections = 100`).
2. PgBouncer 1.24 in transaction mode, `default_pool_size = 25`.
3. pgmux in transaction mode, pool size 25.

All three run in Docker on one network (`docker-compose.bench.yml`). pgbench runs in its own container.

## Run

```sh
bench/run.sh                                   # full plan: 3 runs x 60 s per point, 15 s warm-up
CLIENTS="10 100 1000" RUNS=1 DURATION=20 bench/run.sh   # quick run
uv run --with matplotlib python3 bench/plot.py bench/results/raw/<run>/results.csv
```

Settings are environment variables: `CLIENTS`, `SETUPS`, `WORKLOADS` (`select`, `tpcb`),
`PROTOCOLS` (`simple`, `prepared`), `DURATION`, `WARMUP`, `RUNS`, `SCALE`, `JOBS`.
To pin CPUs, set `PG_CPUS`, `POOLER_CPUS` and `CLIENT_CPUS` (for example `0-3`, `4-5`, `6-7`).
For credible numbers, use separate machines or pinned CPUs, and commit `specs.txt` and the CSV files.

Each run writes `results.csv` (one row per run), `specs.txt`, and the raw pgbench logs.
`plot.py` writes `summary.csv` (median per point), `tps_vs_clients.png` and `p99_vs_clients.png`.

## Connection churn

`churn` opens a connection, runs one query and closes it, in a loop. It simulates serverless apps.

```sh
go run ./bench/churn -dsn "postgres://app:app@localhost:6432/appdb?sslmode=disable" -workers 50 -duration 30s
```

On macOS, a high churn rate runs out of local ephemeral ports. Run it in a Linux container for
large tests.
