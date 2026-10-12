// Shared client scenarios for pgmux with node-postgres, postgres.js and Prisma.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { Readable, Writable } from "node:stream";
import { pipeline } from "node:stream/promises";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { PrismaClient } from "@prisma/client";
import pg from "pg";
import { from as copyFrom, to as copyTo } from "pg-copy-streams";
import QueryStream from "pg-query-stream";
import postgres from "postgres";

const host = process.env.PGMUX_HOST ?? "localhost";
const port = Number(process.env.PGMUX_PORT ?? "6432");
const mode = process.env.PGMUX_MODE ?? "transaction";
const database = { transaction: "appdb", session: "sessiondb" }[mode];
const here = dirname(fileURLToPath(import.meta.url));
const ca = process.env.PGMUX_CA ?? join(here, "../../../deploy/certs/ca.crt");
const pgConfig = { host, port, database, user: "app", password: "app" };

let seq = 0;
const table = (prefix) => `node_${prefix}_${process.pid}_${seq++}`;

async function withClient(fn, config = pgConfig) {
  const client = new pg.Client(config);
  await client.connect();
  try {
    return await fn(client);
  } finally {
    await client.end();
  }
}

test("basic_crud: pg", () =>
  withClient(async (c) => {
    const t = table("crud");
    await c.query(`CREATE TABLE ${t} (id int PRIMARY KEY, name text)`);
    await c.query(`INSERT INTO ${t} VALUES ($1, $2), ($3, $4)`, [1, "a", 2, "b"]);
    await c.query(`UPDATE ${t} SET name = $1 WHERE id = $2`, ["z", 2]);
    const { rows } = await c.query(`SELECT name FROM ${t} WHERE id = 2`);
    assert.equal(rows[0].name, "z");
    await c.query(`DELETE FROM ${t} WHERE id = 1`);
    await c.query(`DROP TABLE ${t}`);
  }));

test("basic_crud: postgres.js", async () => {
  const sql = postgres({ ...pgConfig, username: "app", max: 1 });
  const t = table("pjs");
  try {
    await sql.unsafe(`CREATE TABLE ${t} (id int PRIMARY KEY, name text)`);
    await sql`INSERT INTO ${sql(t)} ${sql([{ id: 1, name: "a" }, { id: 2, name: "b" }])}`;
    await sql`UPDATE ${sql(t)} SET name = ${"z"} WHERE id = ${2}`;
    const [row] = await sql`SELECT name FROM ${sql(t)} WHERE id = ${2}`;
    assert.equal(row.name, "z");
    await sql.unsafe(`DROP TABLE ${t}`);
  } finally {
    await sql.end();
  }
});

test("basic_crud: prisma", async () => {
  const url = `postgresql://app:app@${host}:${port}/${database}?sslmode=disable`;
  await withClient((c) => c.query("CREATE TABLE IF NOT EXISTS prisma_items (id serial PRIMARY KEY, name text NOT NULL)"));
  const prisma = new PrismaClient({ datasources: { db: { url } } });
  try {
    const item = await prisma.prismaItem.create({ data: { name: "a" } });
    await prisma.prismaItem.update({ where: { id: item.id }, data: { name: "z" } });
    const found = await prisma.prismaItem.findUnique({ where: { id: item.id } });
    assert.equal(found.name, "z");
    // An interactive transaction holds one server until it ends.
    await prisma.$transaction(async (tx) => {
      await tx.prismaItem.create({ data: { name: "in-tx" } });
      await tx.prismaItem.deleteMany({ where: { name: "in-tx" } });
    });
    await prisma.prismaItem.delete({ where: { id: item.id } });
  } finally {
    await prisma.$disconnect();
  }
});

test("transaction_commit_rollback: pg", () =>
  withClient(async (c) => {
    const t = table("tx");
    await c.query(`CREATE TABLE ${t} (v int)`);
    await c.query("BEGIN");
    await c.query(`INSERT INTO ${t} VALUES (1)`);
    await c.query("COMMIT");
    await c.query("BEGIN");
    await c.query(`INSERT INTO ${t} VALUES (2)`);
    await c.query("ROLLBACK");
    await c.query("BEGIN");
    await assert.rejects(c.query("SELECT 1/0"), /division by zero/);
    await c.query("ROLLBACK");
    const { rows } = await c.query(`SELECT count(*)::int AS n FROM ${t}`);
    assert.equal(rows[0].n, 1);
    await c.query(`DROP TABLE ${t}`);
  }));

test("prepared_statements: pg named queries across reconnects", async () => {
  for (let round = 0; round < 3; round++) {
    await withClient(async (c) => {
      for (let i = 0; i < 100; i++) {
        const { rows } = await c.query({ name: "add_one", text: "SELECT $1::int + 1 AS v", values: [i] });
        assert.equal(rows[0].v, i + 1);
      }
    });
  }
});

test("prepared_statements: postgres.js pipelined", async () => {
  const sql = postgres({ ...pgConfig, username: "app", max: 1, prepare: true });
  try {
    // postgres.js pipelines these queries on one connection.
    const results = await Promise.all(Array.from({ length: 100 }, (_, i) => sql`SELECT ${i}::int + 1 AS v`));
    results.forEach(([row], i) => assert.equal(row.v, i + 1));
  } finally {
    await sql.end();
  }
});

test("concurrency: pg 200 workers x 50 transactions", async () => {
  const t = table("conc");
  await withClient(async (c) => {
    await c.query(`CREATE TABLE ${t} (id int PRIMARY KEY, n int)`);
    await c.query(`INSERT INTO ${t} VALUES (1, 0)`);
  });
  await Promise.all(
    Array.from({ length: 200 }, () =>
      withClient(async (c) => {
        for (let i = 0; i < 50; i++) {
          await c.query("BEGIN");
          await c.query(`UPDATE ${t} SET n = n + 1 WHERE id = 1`);
          await c.query("COMMIT");
        }
      }),
    ),
  );
  await withClient(async (c) => {
    const { rows } = await c.query(`SELECT n FROM ${t}`);
    assert.equal(rows[0].n, 10000);
    await c.query(`DROP TABLE ${t}`);
  });
});

test("cancel_query: postgres.js", async () => {
  const sql = postgres({ ...pgConfig, username: "app", max: 1 });
  try {
    const q = sql`SELECT pg_sleep(30)`;
    setTimeout(() => q.cancel(), 300);
    const start = Date.now();
    await assert.rejects(q, (err) => err.code === "57014");
    assert.ok(Date.now() - start < 5000);
    const [row] = await sql`SELECT 1 AS v`;
    assert.equal(row.v, 1);
  } finally {
    await sql.end();
  }
});

