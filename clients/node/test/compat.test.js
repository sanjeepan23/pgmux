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

