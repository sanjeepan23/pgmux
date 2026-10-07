"""Shared client scenarios for pgmux with psycopg 3, asyncpg and SQLAlchemy 2."""

import asyncio
import io
import itertools
import os
import pathlib
import threading
import time

import asyncpg
import psycopg
import pytest
import sqlalchemy as sa

HOST = os.environ.get("PGMUX_HOST", "localhost")
PORT = int(os.environ.get("PGMUX_PORT", "6432"))
MODE = os.environ.get("PGMUX_MODE", "transaction")
DB = {"transaction": "appdb", "session": "sessiondb"}[MODE]
CA = os.environ.get("PGMUX_CA", str(pathlib.Path(__file__).parents[3] / "deploy/certs/ca.crt"))
DSN = f"host={HOST} port={PORT} user=app password=app dbname={DB} sslmode=disable"
URL = f"postgresql://app:app@{HOST}:{PORT}/{DB}?sslmode=disable"

_seq = itertools.count()


def table(prefix):
    return f"py_{prefix}_{os.getpid()}_{next(_seq)}"


