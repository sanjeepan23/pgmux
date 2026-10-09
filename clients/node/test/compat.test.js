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
