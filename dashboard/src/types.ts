export interface Stats {
  version: string;
  uptime_seconds: number;
  clients: number;
  clients_waiting: number;
  servers_active: number;
  servers_idle: number;
  pools: number;
  queries_total: number;
  transactions_total: number;
  errors_total: number;
}

export interface PoolStats {
  user: string;
  database: string;
  mode: string;
  paused: boolean;
  max_size: number;
  min_size: number;
  clients: number;
  clients_waiting: number;
  servers_total: number;
  servers_active: number;
  servers_idle: number;
}

