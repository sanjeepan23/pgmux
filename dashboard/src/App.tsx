import { useEffect, useRef, useState } from "react";

import { LineChart } from "./LineChart";
import type { Event, PoolStats, Sample } from "./types";

// Keep five minutes of one-second samples.
const HISTORY = 300;

// The admin API address. Default is the page origin, where pgmux serves the dashboard.
const api = new URLSearchParams(location.search).get("api") ?? "";

function useEvents() {
  const [latest, setLatest] = useState<Event | null>(null);
  const [samples, setSamples] = useState<Sample[]>([]);
  const [connected, setConnected] = useState(false);
  const prev = useRef<Event | null>(null);

  useEffect(() => {
    const es = new EventSource(`${api}/events`);
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);
    es.onmessage = (msg) => {
      const ev: Event = JSON.parse(msg.data);
      const p = prev.current;
      prev.current = ev;
      setLatest(ev);
      if (!p) return;
      const dt = Math.max(0.001, (ev.time - p.time) / 1000);
      const sample: Sample = {
        time: ev.time,
        qps: Math.max(0, (ev.stats.queries_total - p.stats.queries_total) / dt),
        tps: Math.max(0, (ev.stats.transactions_total - p.stats.transactions_total) / dt),
        serversActive: ev.stats.servers_active,
        serversMax: ev.pools.reduce((sum, pool) => sum + pool.max_size, 0),
        clientsWaiting: ev.stats.clients_waiting,
      };
      setSamples((s) => [...s.slice(-(HISTORY - 1)), sample]);
    };
    return () => es.close();
  }, []);

  return { latest, samples, connected };
}

function Tile({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="card tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
      {detail && <div className="tile-detail">{detail}</div>}
    </div>
  );
}

function PoolTable({ pools }: { pools: PoolStats[] }) {
  return (
    <section className="card">
      <h2>Pools</h2>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Database</th>
              <th>Mode</th>
              <th className="num">Clients</th>
              <th className="num">Waiting</th>
              <th className="num">Active</th>
              <th className="num">Idle</th>
              <th className="num">Max</th>
              <th>Saturation</th>
            </tr>
          </thead>
          <tbody>
            {pools.map((p) => {
              const pct = p.max_size ? Math.round((p.servers_active / p.max_size) * 100) : 0;
              return (
                <tr key={`${p.user}/${p.database}`}>
                  <td>{p.user}</td>
                  <td>{p.database}</td>
                  <td>
                    {p.mode}
                    {p.paused && <span className="badge">paused</span>}
                  </td>
                  <td className="num">{p.clients}</td>
                  <td className="num">{p.clients_waiting}</td>
                  <td className="num">{p.servers_active}</td>
                  <td className="num">{p.servers_idle}</td>
                  <td className="num">{p.max_size}</td>
                  <td>
                    <div className="bar" role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label="servers in use">
                      <div className="bar-fill" style={{ width: `${pct}%` }} />
                    </div>
                    <span className="bar-label">{pct}%</span>
                  </td>
                </tr>
              );
            })}
            {pools.length === 0 && (
              <tr>
                <td colSpan={9} className="empty">
                  No pools yet. A pool starts when the first client connects.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

export function App() {
  const { latest, samples, connected } = useEvents();
  const s = latest?.stats;
  const last = samples[samples.length - 1];
  const times = samples.map((p) => p.time);

  return (
    <main>
      <header>
        <h1>pgmux</h1>
        <span className={connected ? "status ok" : "status off"}>
          <span aria-hidden="true">{connected ? "●" : "○"}</span> {connected ? "live" : "disconnected"}
        </span>
        {s && <span className="meta">version {s.version} · up {Math.floor(s.uptime_seconds / 60)} min</span>}
      </header>

      <section className="tiles">
        <Tile label="Clients" value={s ? `${s.clients}` : "–"} detail={s ? `${s.clients_waiting} waiting` : undefined} />
        <Tile
          label="Server connections"
          value={s ? `${s.servers_active}` : "–"}
          detail={s ? `active, ${s.servers_idle} idle` : undefined}
        />
        <Tile label="Queries per second" value={last ? Math.round(last.qps).toLocaleString() : "–"} />
        <Tile label="Transactions per second" value={last ? Math.round(last.tps).toLocaleString() : "–"} />
        <Tile label="Errors" value={s ? s.errors_total.toLocaleString() : "–"} detail="since start" />
      </section>

      <div className="charts">
        <LineChart
          title="Throughput"
          unit="per second"
          times={times}
          series={[
            { key: "qps", label: "queries/s", color: "var(--series-1)", values: samples.map((p) => p.qps) },
            { key: "tps", label: "transactions/s", color: "var(--series-2)", values: samples.map((p) => p.tps) },
          ]}
        />
        <LineChart
          title="Pool saturation"
          unit="connections"
          times={times}
          series={[
            { key: "active", label: "servers active", color: "var(--series-1)", values: samples.map((p) => p.serversActive) },
            { key: "waiting", label: "clients waiting", color: "var(--series-2)", values: samples.map((p) => p.clientsWaiting) },
          ]}
          reference={last ? { label: "pool max", value: last.serversMax } : undefined}
        />
      </div>

      <PoolTable pools={latest?.pools ?? []} />
    </main>
  );
}
