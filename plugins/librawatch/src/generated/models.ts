// Generated from docs/openapi.yaml. Do not hand-edit shapes that don't exist
// in the spec — if the API changes, update the OpenAPI file first, then this.

// ─── Enum-like string unions (from OpenAPI `enum`s) ────────────────────────

export type NetworkMode = "ethernet" | "wifi" | "both";
export type ApplicationStatus = "pending_review" | "allowed" | "blocked" | "ignored";
export type PolicyAppStatus = "" | ApplicationStatus;
export type PolicyExecutionLocation = "" | "downloads" | "desktop" | "temp" | "usb";
export type PolicyAction = "log" | "notify" | "block" | "delete" | "kill";
export type EventAction = "log" | "notify" | "blocked" | "deleted" | "killed";
export type AlertType =
  | "cpu_high"
  | "ram_high"
  | "blacklisted_app"
  | "offline"
  | "recovery"
  | "peripheral_removed";
export type DeployType = "exec" | "winget" | "file_deploy" | "deepfreeze" | "install_ssh";
export type DeployJobStatus = "pending" | "running" | "done" | "cancelled";
export type DeployResultStatus = "pending" | "running" | "success" | "failed" | "cancelled";

// ─── Common wrappers ────────────────────────────────────────────────────────

export interface ErrorResponse {
  error: string;
}

export interface OkResponse {
  ok: boolean;
}

// ─── Entities ───────────────────────────────────────────────────────────────

export interface Agent {
  id: string;
  hostname: string;
  ip: string;
  os: string;
  last_seen: string;
  mesh_id: string;
  status: string;
  created_at: string;
  agent_version: string;
  windows_version: string;
  disk_capacity_gb: number;
  device_group: string;
  mac_address: string;
  floor: string;
  desired_network_mode: NetworkMode;
  current_network_mode: string;
  network_mode_status: string;
  network_mode_detail: string;
  network_mode_updated_at: string;
}

export interface AgentWithMetrics extends Agent {
  cpu: number;
  ram: number;
  top_process: string;
  installed_software_count: number;
  running_process_count: number;
}

export interface Metric {
  id: number;
  agent_id: string;
  cpu: number;
  ram: number;
  recorded_at: string;
}

export interface Process {
  name: string;
  pid: number;
  cpu: number;
  ram: number;
  path?: string;
  product_name?: string;
  company?: string;
  description?: string;
  product_version?: string;
  size?: number;
  file_created_at?: string;
  file_modified_at?: string;
}

export interface Category {
  id: number;
  name: string;
}

export interface Application {
  id: number;
  exe_name: string;
  company: string;
  product_name: string;
  description: string;
  product_version: string;
  category_id: number | null;
  status: ApplicationStatus;
  created_at: string;
  updated_at: string;
}

export interface ApplicationWithStats extends Application {
  category_name: string;
  device_count: number;
  total_executions: number;
  first_seen: string;
  last_seen: string;
}

export interface AppSighting {
  agent_id: string;
  hostname: string;
  application_id: number;
  path: string;
  size: number;
  file_created_at: string;
  file_modified_at: string;
  first_seen: string;
  last_seen: string;
  exec_count: number;
}

export interface ApplicationDetail extends ApplicationWithStats {
  sightings: AppSighting[];
}

export interface Event {
  id: number;
  agent_id: string;
  hostname?: string;
  type: string;
  metadata: string;
  action: EventAction;
  created_at: string;
}

export interface PolicyRuleInput {
  name: string;
  event_type?: string;
  category_id?: number | null;
  app_status?: PolicyAppStatus;
  file_extension?: string;
  execution_location?: PolicyExecutionLocation;
  device_group?: string;
  action: PolicyAction;
  enabled?: boolean;
}

export interface PolicyRule extends PolicyRuleInput {
  id: number;
  created_at: string;
}

export interface Alert {
  id: number;
  agent_id: string;
  type: AlertType;
  message: string;
  sent_at: string;
}

export interface DeployJob {
  id: string;
  type: DeployType;
  payload: string;
  args: string;
  targets: string;
  status: DeployJobStatus;
  priority: number;
  expire_at?: string | null;
  created_by: string;
  created_at: string;
}

export interface DeployResult {
  id: number;
  job_id: string;
  agent_id: string;
  status: DeployResultStatus;
  output: string;
  executed_at?: string | null;
  lease_until?: string | null;
  retry_count: number;
  max_retry: number;
  exit_code?: number | null;
  duration_ms?: number | null;
}

export interface AuditLog {
  id: number;
  ts: string;
  action: string;
  target: string;
  detail: string;
  ip: string;
}

export interface ClientSummary {
  id: string;
  hostname: string;
  ip: string;
  mac_address: string;
  os: string;
  agent_version: string;
  status: string;
  floor: string;
  last_seen: string;
}

// ─── Auth ───────────────────────────────────────────────────────────────────

export interface LoginRequest {
  username: string;
  password: string;
}

export interface LoginResponse {
  token: string;
}

export interface LogoutResponse {
  message: string;
}

// ─── Agents ─────────────────────────────────────────────────────────────────

export type ListAgentsResponse = AgentWithMetrics[];
export type GetAgentResponse = AgentWithMetrics;

export interface UpdateAgentRequest {
  mesh_id?: string | null;
  device_group?: string | null;
  floor?: string | null;
}
export type UpdateAgentResponse = OkResponse;
export type DeleteAgentResponse = OkResponse;

export type ListAgentMetricsResponse = Metric[];
export type ListAgentProcessesResponse = Process[];

export interface KillProcessRequest {
  pid?: number;
  name?: string;
}
export interface KillProcessResponse {
  output: string;
}

export interface SetNetworkModeRequest {
  mode: NetworkMode;
}
export interface NetworkModeResult {
  network_mode?: string;
  status?: string;
  output?: string;
}
export interface SetNetworkModeResponse {
  ok: boolean;
  desired_network_mode: NetworkMode;
  applied_live: boolean;
  message?: string;
  result?: NetworkModeResult;
}

export interface AgentLogsQuery {
  lines?: number;
}
export interface AgentLogsResponse {
  lines: string;
}

export interface AgentEventsQuery {
  type?: string;
  limit?: number;
}

// ─── Alerts ─────────────────────────────────────────────────────────────────

export interface ListAlertsQuery {
  limit?: number;
}
export type ListAlertsResponse = Alert[];

// ─── Applications / Categories ─────────────────────────────────────────────

export interface ListApplicationsQuery {
  status?: ApplicationStatus;
  category_id?: number;
}
export type ListApplicationsResponse = ApplicationWithStats[];
export type GetApplicationResponse = ApplicationDetail;

export interface UpdateApplicationRequest {
  status: ApplicationStatus;
  category_id?: number | null;
}
export type UpdateApplicationResponse = OkResponse;

export type ListCategoriesResponse = Category[];

// ─── Events (cross-agent) ───────────────────────────────────────────────────

export interface ListEventsQuery {
  agent_id?: string;
  type?: string;
  limit?: number;
}
export type ListEventsResponse = Event[];

// ─── Policy Rules ───────────────────────────────────────────────────────────

export type ListPolicyRulesResponse = PolicyRule[];
export type CreatePolicyRuleRequest = PolicyRuleInput;
export type CreatePolicyRuleResponse = PolicyRule;
export type UpdatePolicyRuleRequest = PolicyRuleInput;
export type UpdatePolicyRuleResponse = OkResponse;
export type DeletePolicyRuleResponse = OkResponse;

// ─── Settings ───────────────────────────────────────────────────────────────

export type GetSettingsResponse = Record<string, string>;
export type UpdateSettingsRequest = Record<string, unknown>;
export type UpdateSettingsResponse = OkResponse;

// ─── Audit ──────────────────────────────────────────────────────────────────

export interface ListAuditLogsQuery {
  limit?: number;
}
export type ListAuditLogsResponse = AuditLog[];

// ─── Health / Stats ─────────────────────────────────────────────────────────

export interface HealthResponse {
  ok: boolean;
  uptime: string;
  agents_online: number;
}
export interface StatsResponse {
  online: number;
  today_alerts: number;
}

// ─── Deploy ─────────────────────────────────────────────────────────────────

export type ListDeployJobsResponse = DeployJob[];

export interface CreateDeployJobRequest {
  type: DeployType;
  payload?: string;
  args?: string;
  targets: string[];
  priority?: number;
  expire_at?: string;
  max_retry?: number | null;
}
export type CreateDeployJobResponse = DeployJob;

export interface GetDeployJobResponse {
  job: DeployJob;
  results: DeployResult[];
}
export type CancelDeployJobResponse = OkResponse;

export interface UploadResponse {
  filename: string;
}

// ─── Notifications ──────────────────────────────────────────────────────────

export type TestTelegramResponse = OkResponse;
export type TestEmailResponse = OkResponse;

// ─── Logs ───────────────────────────────────────────────────────────────────

export interface TailLogsQuery {
  lines?: number;
}
export interface TailLogsResponse {
  lines: string;
}

// ─── Clients v1 ─────────────────────────────────────────────────────────────

export interface ListClientsResponse {
  success: boolean;
  data: ClientSummary[];
}
