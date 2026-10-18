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


