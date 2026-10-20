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
echo "setup,workload,protocol,clients,run,tps,latency_avg_ms,p50_ms,p95_ms,p99_ms,failed,conn_errors,aborted,pooler_cpu_pct,pooler_rss_mb" > "$CSV"

$COMPOSE up -d --build --wait
pin() { [ -n "${2:-}" ] && docker update --cpuset-cpus "$2" "pgmux-bench-$1-1" >/dev/null || true; }
pin postgres "${PG_CPUS:-}"
pin pgmux "${POOLER_CPUS:-}"
pin pgbouncer "${POOLER_CPUS:-}"

pgbench() {
  docker run --rm --network "$NETWORK" --ulimit nofile=65536:65536 ${CLIENT_CPUS:+--cpuset-cpus "$CLIENT_CPUS"} \
    -e PGPASSWORD=app -v "$PWD/$OUT/logs:/logs" "$PGBENCH_IMAGE" pgbench "$@"
}

{
  echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "host: $(uname -srm)"
  echo "docker: $(docker info --format 'cpus={{.NCPU}} mem={{.MemTotal}} os={{.OperatingSystem}}')"
  echo "pgbench image: $PGBENCH_IMAGE"
  echo "settings: duration=${DURATION}s warmup=${WARMUP}s runs=$RUNS scale=$SCALE jobs=$JOBS"
  echo "pinning: postgres=${PG_CPUS:-none} pooler=${POOLER_CPUS:-none} client=${CLIENT_CPUS:-none}"
} > "$OUT/specs.txt"
cat "$OUT/specs.txt"

echo "initializing pgbench tables (scale $SCALE)"
pgbench -i -q -s "$SCALE" -h postgres -U app appdb >/dev/null 2>&1

target() {
  case $1 in
    direct) echo "postgres 5432 postgres" ;;
    pgbouncer) echo "pgbouncer 6433 pgbouncer" ;;
    pgmux) echo "pgmux 6432 pgmux" ;;
  esac
}

for setup in $SETUPS; do
  read -r host port container <<<"$(target "$setup")"
  for workload in $WORKLOADS; do
    flag=""
    [ "$workload" = select ] && flag="-S"
    for protocol in $PROTOCOLS; do
      for clients in $CLIENTS; do
        jobs=$((clients < JOBS ? clients : JOBS))
        args=(-n $flag -M "$protocol" -c "$clients" -j "$jobs" -h "$host" -p "$port" -U app appdb)
        echo "== $setup $workload $protocol clients=$clients"
        pgbench "${args[@]}" -T "$WARMUP" >/dev/null 2>&1 || true
        for run in $(seq 1 "$RUNS"); do
          prefix="$setup-$workload-$protocol-$clients-$run"
          rm -f "$OUT/logs/$prefix".*
          # Sample the pooler's CPU and memory in the middle of the run.
          stats_file="$OUT/logs/$prefix.stats"
          (sleep $((DURATION / 2)); docker stats --no-stream --format '{{.CPUPerc}} {{.MemUsage}}' "pgmux-bench-$container-1" > "$stats_file" 2>/dev/null) &
          output=$(pgbench "${args[@]}" -T "$DURATION" --log --sampling-rate="$SAMPLING" --log-prefix="/logs/$prefix" 2>&1 || true)
          wait
          echo "$output" > "$OUT/logs/$prefix.out"
          tps=$(echo "$output" | awk '/^tps = /{print $3; exit}')
          lat=$(echo "$output" | awk '/^latency average = /{print $4; exit}')
          failed=$(echo "$output" | awk '/number of failed transactions/{print $5; exit}')
          conn_errors=$(echo "$output" | grep -c -E "connection to server|too many clients|could not connect" || true)
          # Clients that lost their connection during the run. pgbench keeps going with the rest.
          aborted=$(echo "$output" | grep -c -E "client [0-9]+ aborted" || true)
          read -r p50 p95 p99 <<<"$(python3 percentiles.py "$OUT/logs/$prefix".* 2>/dev/null || echo "  ")"
          cpu="" rss=""
          if [ "$setup" != direct ] && [ -s "$stats_file" ]; then
            read -r cpu rss <<<"$(awk '{gsub("%","",$1); print $1, $2}' "$stats_file")"
            rss=$(python3 -c "import re,sys; v,u=re.match(r'([\d.]+)(\w+)', sys.argv[1]).groups(); print(round(float(v)*{'B':1/2**20,'KiB':1/1024,'MiB':1,'GiB':1024}[u],1))" "$rss")
          fi
          echo "$setup,$workload,$protocol,$clients,$run,${tps:-0},${lat:-},${p50:-},${p95:-},${p99:-},${failed:-},$conn_errors,$aborted,$cpu,$rss" | tee -a "$CSV"
        done
      done
    done
  done
done

echo "results: $CSV"
echo "plot with: uv run --with matplotlib python3 bench/plot.py $CSV"
