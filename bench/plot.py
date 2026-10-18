"""Write the median of each benchmark point to summary.csv, and plot TPS and p99 latency against client count."""

import csv
import pathlib
import statistics
import sys
from collections import defaultdict

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt

COLORS = {"direct": "#7f7f7f", "pgbouncer": "#1f77b4", "pgmux": "#d62728"}


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def main(csv_path):
    path = pathlib.Path(csv_path)
    groups = defaultdict(list)
    with path.open() as f:
        for row in csv.DictReader(f):
            groups[(row["setup"], row["workload"], row["protocol"], int(row["clients"]))].append(row)

    fields = ["tps", "latency_avg_ms", "p50_ms", "p95_ms", "p99_ms", "failed", "conn_errors", "pooler_cpu_pct", "pooler_rss_mb"]
    summary = []
    for key, rows in sorted(groups.items()):
        med = {}
        for fld in fields:
            vals = [v for v in (num(r[fld]) for r in rows) if v is not None]
            med[fld] = statistics.median(vals) if vals else None
        summary.append((key, med))

    out_csv = path.with_name("summary.csv")
    with out_csv.open("w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["setup", "workload", "protocol", "clients"] + fields)
        for key, med in summary:
            w.writerow(list(key) + ["" if med[k] is None else round(med[k], 2) for k in fields])

    panels = sorted({(k[1], k[2]) for k, _ in summary})
    for metric, label, fname in [("tps", "transactions per second", "tps_vs_clients.png"), ("p99_ms", "p99 latency (ms)", "p99_vs_clients.png")]:
        fig, axes = plt.subplots(1, len(panels), figsize=(5 * len(panels), 4), squeeze=False)
        for ax, (workload, protocol) in zip(axes[0], panels):
            for setup in COLORS:
                pts = [(k[3], m[metric]) for k, m in summary if k[:3] == (setup, workload, protocol) and m[metric]]
                if pts:
                    xs, ys = zip(*pts)
                    ax.plot(xs, ys, marker="o", label=setup, color=COLORS[setup])
            ax.set_xscale("log")
            if metric == "p99_ms":
                ax.set_yscale("log")
            ax.set_title(f"{workload}, -M {protocol}")
            ax.set_xlabel("clients")
            ax.set_ylabel(label)
            ax.grid(True, alpha=0.3)
            ax.legend()
        fig.tight_layout()
        fig.savefig(path.with_name(fname), dpi=120)
    print(f"wrote {out_csv} and charts in {path.parent}")


if __name__ == "__main__":
    main(sys.argv[1])
