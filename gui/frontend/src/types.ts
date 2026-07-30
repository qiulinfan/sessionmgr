export type HealthStatus = "ready" | "warning" | "failed";

export interface HealthCheck {
  id: string;
  label: string;
  status: HealthStatus;
  detail: string;
}

export interface DashboardStats {
  runs: number;
  verified: number;
  needs_attention: number;
  stores: number;
}

export interface RunCard {
  id: string;
  title: string;
  repository: string;
  agent: string;
  created_at: string;
  integrity: string;
  sync_status: string;
  relation: string;
}

export interface StoreCard {
  name: string;
  type: string;
  status: string;
  detail: string;
}

export interface Dashboard {
  schema_version: number;
  preview: boolean;
  version: string;
  home: string;
  health: HealthCheck[];
  stats: DashboardStats;
  recent_runs: RunCard[];
  stores: StoreCard[];
}
