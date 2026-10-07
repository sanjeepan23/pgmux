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


@pytest.fixture
def conn():
    with psycopg.connect(DSN, autocommit=True) as c:
        yield c


# basic_crud


def test_basic_crud_psycopg(conn):
    t = table("crud")
    conn.execute(f"CREATE TABLE {t} (id int PRIMARY KEY, name text)")
    try:
        conn.execute(f"INSERT INTO {t} VALUES (%s, %s), (%s, %s)", (1, "a", 2, "b"))
        conn.execute(f"UPDATE {t} SET name = %s WHERE id = %s", ("z", 2))
        assert conn.execute(f"SELECT name FROM {t} WHERE id = 2").fetchone()[0] == "z"
        conn.execute(f"DELETE FROM {t} WHERE id = 1")
        assert conn.execute(f"SELECT count(*) FROM {t}").fetchone()[0] == 1
    finally:
        conn.execute(f"DROP TABLE {t}")


def test_basic_crud_sqlalchemy():
    engine = sa.create_engine(URL.replace("postgresql://", "postgresql+psycopg://"))
    meta = sa.MetaData()
    items = sa.Table(table("sa"), meta, sa.Column("id", sa.Integer, primary_key=True), sa.Column("name", sa.Text))
    meta.create_all(engine)
    try:
        with engine.begin() as c:
            c.execute(items.insert(), [{"id": 1, "name": "a"}, {"id": 2, "name": "b"}])
            c.execute(items.update().where(items.c.id == 2).values(name="z"))
        with engine.connect() as c:
            assert c.execute(sa.select(items.c.name).where(items.c.id == 2)).scalar_one() == "z"
    finally:
        meta.drop_all(engine)
        engine.dispose()


async def test_basic_crud_asyncpg():
    c = await asyncpg.connect(URL)
    t = table("apg")
    try:
        await c.execute(f"CREATE TABLE {t} (id int PRIMARY KEY, name text)")
        await c.executemany(f"INSERT INTO {t} VALUES ($1, $2)", [(1, "a"), (2, "b")])
        await c.execute(f"UPDATE {t} SET name = $1 WHERE id = $2", "z", 2)
        assert await c.fetchval(f"SELECT name FROM {t} WHERE id = $1", 2) == "z"
        await c.execute(f"DROP TABLE {t}")
    finally:
        await c.close()


# transaction_commit_rollback


def test_transaction_commit_rollback(conn):
    t = table("tx")
    conn.execute(f"CREATE TABLE {t} (v int)")
    try:
        with conn.transaction():
            conn.execute(f"INSERT INTO {t} VALUES (1)")
        with pytest.raises(RuntimeError):
            with conn.transaction():
                conn.execute(f"INSERT INTO {t} VALUES (2)")
                raise RuntimeError("roll back")
        with pytest.raises(psycopg.errors.DivisionByZero):
            with conn.transaction():
                conn.execute("SELECT 1/0")
        assert conn.execute(f"SELECT count(*) FROM {t}").fetchone()[0] == 1
    finally:
        conn.execute(f"DROP TABLE {t}")


# prepared_statements


