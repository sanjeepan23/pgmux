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

