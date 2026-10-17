#!/usr/bin/env bash
# Benchmark direct Postgres, PgBouncer and pgmux with pgbench, and write one CSV row per run.
# Each point runs RUNS times for DURATION seconds after a WARMUP run. bench/plot.py takes the median.
set -euo pipefail
cd "$(dirname "$0")"

CLIENTS=${CLIENTS:-"10 50 100 500 1000 2000 5000"}
SETUPS=${SETUPS:-"direct pgbouncer pgmux"}
WORKLOADS=${WORKLOADS:-"select tpcb"}
PROTOCOLS=${PROTOCOLS:-"simple prepared"}
DURATION=${DURATION:-60}
WARMUP=${WARMUP:-15}
RUNS=${RUNS:-3}
SCALE=${SCALE:-50}
JOBS=${JOBS:-8}
SAMPLING=${SAMPLING:-0.1}
PGBENCH_IMAGE=${PGBENCH_IMAGE:-postgres:17}

COMPOSE="docker compose -f docker-compose.bench.yml"
NETWORK=pgmux-bench_default
OUT="results/raw/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT/logs"
CSV="$OUT/results.csv"
echo "setup,workload,protocol,clients,run,tps,latency_avg_ms,p50_ms,p95_ms,p99_ms,failed,conn_errors,pooler_cpu_pct,pooler_rss_mb" > "$CSV"

$COMPOSE up -d --build --wait
pin() { [ -n "${2:-}" ] && docker update --cpuset-cpus "$2" "pgmux-bench-$1-1" >/dev/null || true; }
pin postgres "${PG_CPUS:-}"
pin pgmux "${POOLER_CPUS:-}"
pin pgbouncer "${POOLER_CPUS:-}"

