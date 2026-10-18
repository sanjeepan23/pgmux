"""Print p50, p95 and p99 latency in ms from pgbench --log files."""

import sys


def main(paths):
    lat = []
    for path in paths:
        if path.endswith((".out", ".stats")):
            continue
        with open(path) as f:
            for line in f:
                parts = line.split()
                # The third field is the transaction latency in microseconds.
                if len(parts) >= 3 and parts[2].isdigit():
                    lat.append(int(parts[2]))
    if not lat:
        return
    lat.sort()
    pick = lambda q: lat[min(len(lat) - 1, int(q * len(lat)))] / 1000
    print(f"{pick(0.50):.2f} {pick(0.95):.2f} {pick(0.99):.2f}")


if __name__ == "__main__":
    main(sys.argv[1:])
