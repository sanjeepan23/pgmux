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

