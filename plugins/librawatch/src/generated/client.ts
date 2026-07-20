// Generated HTTP client for the LibraWatch REST API (docs/openapi.yaml).
// Contains ONLY HTTP communication — no tool logic, no business logic.
//
// Excluded on purpose (not REST data resources, not modelable as request/response):
//   GET /ws              — WebSocket upgrade
//   GET /                — dashboard index.html
//   GET /static/{file}   — static asset serving

import type { LibraWatchConfig } from "../config.js";
import type {
  AgentEventsQuery,
  AgentLogsQuery,
  AgentLogsResponse,
  CancelDeployJobResponse,
  CreateDeployJobRequest,
  CreateDeployJobResponse,
  CreatePolicyRuleRequest,
  CreatePolicyRuleResponse,
  DeleteAgentResponse,
  DeletePolicyRuleResponse,
  GetAgentResponse,
  GetApplicationResponse,
  GetDeployJobResponse,
  GetSettingsResponse,
  HealthResponse,
  KillProcessRequest,
  KillProcessResponse,
  ListAgentMetricsResponse,
  ListAgentProcessesResponse,
  ListAgentsResponse,
  ListAlertsQuery,
  ListAlertsResponse,
  ListApplicationsQuery,
  ListApplicationsResponse,
  ListAuditLogsQuery,
  ListAuditLogsResponse,
  ListCategoriesResponse,
  ListClientsResponse,
  ListDeployJobsResponse,
  ListEventsQuery,
  ListEventsResponse,
  ListPolicyRulesResponse,
  LoginRequest,
  LoginResponse,
  LogoutResponse,
  SetNetworkModeRequest,
  SetNetworkModeResponse,
  StatsResponse,
  TailLogsQuery,
  TailLogsResponse,
  TestEmailResponse,
  TestTelegramResponse,
  UpdateAgentRequest,
  UpdateAgentResponse,
  UpdateApplicationRequest,
  UpdateApplicationResponse,
  UpdatePolicyRuleRequest,
  UpdatePolicyRuleResponse,
  UpdateSettingsRequest,
  UpdateSettingsResponse,
  UploadResponse,
} from "./models.js";

const DEFAULT_TIMEOUT_MS = 15_000;

// Mirrors server/auth.go's `sessionDuration` (8h) — a login token issued by
// POST /api/login is only ever valid for this long. We refresh a bit early
// (REFRESH_MARGIN_MS) so a request is never sent with a token that expires
// mid-flight.
const SESSION_DURATION_MS = 8 * 60 * 60 * 1000;
const REFRESH_MARGIN_MS = 5 * 60 * 1000;

/** Thrown when the server responded with a non-2xx status. */
export class LibraWatchApiError extends Error {
  readonly status: number;
  readonly endpoint: string;
  readonly body: unknown;

  constructor(message: string, status: number, endpoint: string, body: unknown) {
    super(message);
    this.name = "LibraWatchApiError";
    this.status = status;
    this.endpoint = endpoint;
    this.body = body;
  }
}

/** Thrown for anything that never got an HTTP response (DNS, refused connection, timeout). */
export class LibraWatchNetworkError extends Error {
  readonly endpoint: string;
  readonly cause: unknown;

  constructor(message: string, endpoint: string, cause: unknown) {
    super(message);
    this.name = "LibraWatchNetworkError";
    this.endpoint = endpoint;
    this.cause = cause;
  }
}

const STATUS_MESSAGES: Record<number, string> = {
  400: "Bad request",
  401: "Unauthorized — check LIBRAWATCH_API_KEY, or LIBRAWATCH_USERNAME/LIBRAWATCH_PASSWORD",
  403: "Forbidden — IP not in admin whitelist, or insufficient permission",
  404: "Not found",
  409: "Conflict",
  413: "Payload too large",
  429: "Rate limited — too many attempts, try again later",
  500: "Server error",
};

function describeStatus(status: number): string {
  return STATUS_MESSAGES[status] ?? `HTTP ${status}`;
}

interface RequestOptions {
  // `object` (not Record<string, unknown>) so the specific *Query interfaces
  // in models.ts can be passed without each needing its own index signature —
  // this is purely internal URL-building plumbing, read generically below.
  query?: object;
  body?: unknown;
  formData?: FormData;
  /** Defaults to true. Set false for endpoints marked `security: []` in the spec. */
  requiresAuth?: boolean;
}

export class LibraWatchClient {
  private cachedToken?: string;
  private tokenExpiresAt?: number;
  private loginPromise?: Promise<string>;

  constructor(private readonly config: LibraWatchConfig) {}

  // ── Core HTTP plumbing ────────────────────────────────────────────────

  private buildUrl(path: string, query?: object): string {
    const url = new URL(path, this.config.baseUrl + "/");
    if (query) {
      for (const [key, value] of Object.entries(query as Record<string, unknown>)) {
        if (value !== undefined && value !== null) {
          url.searchParams.set(key, String(value));
        }
      }
    }
    return url.toString();
  }

  // ── Token lifecycle ──────────────────────────────────────────────────
  //
  // The server has no refresh-token endpoint and no long-lived static API
  // key for the REST API (only the /mcp endpoint has that, separately) — a
  // token from POST /api/login is only valid for SESSION_DURATION_MS. So
  // when username/password are configured, this client re-logs in for you:
  // proactively just before expiry, and reactively on an unexpected 401
  // (e.g. the server restarted and dropped its in-memory sessions). A
  // statically configured apiKey is used as-is and is NOT auto-refreshed,
  // since we have no credentials to obtain a replacement for it.

  private canAutoRefresh(): boolean {
    return Boolean(this.config.username && this.config.password);
  }

  private async ensureToken(forceRefresh: boolean): Promise<string | undefined> {
    if (this.config.apiKey) return this.config.apiKey;
    if (!this.canAutoRefresh()) return undefined;

    const stillFresh =
      !forceRefresh &&
      this.cachedToken !== undefined &&
      this.tokenExpiresAt !== undefined &&
      Date.now() < this.tokenExpiresAt - REFRESH_MARGIN_MS;
    if (stillFresh) return this.cachedToken;

    // Dedupe concurrent refreshes — several in-flight requests hitting an
    // expired token at once should trigger exactly one login, not one each.
    if (!this.loginPromise) {
      this.loginPromise = this.doLogin().finally(() => {
        this.loginPromise = undefined;
      });
    }
    return this.loginPromise;
  }

  private async doLogin(): Promise<string> {
    const { token } = await this.request<LoginResponse>("POST", "/api/login", {
      body: { username: this.config.username, password: this.config.password },
      requiresAuth: false,
    });
    this.cachedToken = token;
    this.tokenExpiresAt = Date.now() + SESSION_DURATION_MS;
    return token;
  }

  // ── Request execution ────────────────────────────────────────────────

  private async fetchOnce(
    url: string,
    method: string,
    endpoint: string,
    requiresAuth: boolean,
    forceRefresh: boolean,
    body?: unknown,
    formData?: FormData
  ): Promise<Response> {
    const headers: Record<string, string> = {};
    if (requiresAuth) {
      const token = await this.ensureToken(forceRefresh);
      if (token) headers["Authorization"] = `Bearer ${token}`;
    }

    let requestBody: RequestInit["body"];
    if (formData) {
      requestBody = formData; // fetch sets the multipart boundary itself
    } else if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      requestBody = JSON.stringify(body);
    }

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), DEFAULT_TIMEOUT_MS);
    try {
      return await fetch(url, { method, headers, body: requestBody, signal: controller.signal });
    } catch (err) {
      throw new LibraWatchNetworkError(
        `Network error calling ${endpoint}: ${(err as Error).message ?? err}`,
        endpoint,
        err
      );
    } finally {
      clearTimeout(timeout);
    }
  }

  private async request<T>(
    method: string,
    path: string,
    opts: RequestOptions = {}
  ): Promise<T> {
    const { query, body, formData, requiresAuth = true } = opts;
    const url = this.buildUrl(path, query);
    const endpoint = `${method} ${path}`;

    let res = await this.fetchOnce(url, method, endpoint, requiresAuth, false, body, formData);

    // Reactive fallback: an unexpected 401 despite a proactively-fresh token
    // (e.g. the server restarted and dropped its in-memory session map) gets
    // exactly one forced re-login + retry, only when we hold credentials to
    // do so — never an infinite loop, never retried on a static apiKey.
    if (res.status === 401 && requiresAuth && this.canAutoRefresh()) {
      res = await this.fetchOnce(url, method, endpoint, requiresAuth, true, body, formData);
    }

    const contentType = res.headers.get("content-type") ?? "";
    const isJson = contentType.includes("application/json");

    if (!res.ok) {
      const errBody = isJson
        ? await res.json().catch(() => undefined)
        : await res.text().catch(() => undefined);
      const detail =
        errBody && typeof errBody === "object" && "error" in errBody
          ? ` — ${(errBody as { error?: string }).error}`
          : "";
      throw new LibraWatchApiError(
        `${describeStatus(res.status)}${detail} (${endpoint})`,
        res.status,
        endpoint,
        errBody
      );
    }

    if (res.status === 204 || !isJson) {
      return undefined as T;
    }
    return (await res.json()) as T;
  }

  private async requestBinary(path: string, requiresAuth: boolean): Promise<ArrayBuffer> {
    const url = this.buildUrl(path);
    const endpoint = `GET ${path}`;

    let res = await this.fetchOnce(url, "GET", endpoint, requiresAuth, false);
    if (res.status === 401 && requiresAuth && this.canAutoRefresh()) {
      res = await this.fetchOnce(url, "GET", endpoint, requiresAuth, true);
    }

    if (!res.ok) {
      const errBody = await res.text().catch(() => undefined);
      throw new LibraWatchApiError(
        `${describeStatus(res.status)} (${endpoint})`,
        res.status,
        endpoint,
        errBody
      );
    }
    return res.arrayBuffer();
  }

  // ── Auth ───────────────────────────────────────────────────────────────

  login(body: LoginRequest): Promise<LoginResponse> {
    return this.request("POST", "/api/login", { body, requiresAuth: false });
  }

  logout(): Promise<LogoutResponse> {
    return this.request("POST", "/api/logout");
  }

  // ── Agents ───────────────────────────────────────────────────────────

  listAgents(): Promise<ListAgentsResponse> {
    return this.request("GET", "/api/agents");
  }

  getAgent(id: string): Promise<GetAgentResponse> {
    return this.request("GET", `/api/agents/${encodeURIComponent(id)}`);
  }

  updateAgent(id: string, body: UpdateAgentRequest): Promise<UpdateAgentResponse> {
    return this.request("PATCH", `/api/agents/${encodeURIComponent(id)}`, { body });
  }

  deleteAgent(id: string): Promise<DeleteAgentResponse> {
    return this.request("DELETE", `/api/agents/${encodeURIComponent(id)}`);
  }

  listAgentMetrics(id: string): Promise<ListAgentMetricsResponse> {
    return this.request("GET", `/api/agents/${encodeURIComponent(id)}/metrics`);
  }

  listAgentProcesses(id: string): Promise<ListAgentProcessesResponse> {
    return this.request("GET", `/api/agents/${encodeURIComponent(id)}/processes`);
  }

  killProcess(id: string, body: KillProcessRequest): Promise<KillProcessResponse> {
    return this.request("POST", `/api/agents/${encodeURIComponent(id)}/kill`, { body });
  }

  setAgentNetworkMode(
    id: string,
    body: SetNetworkModeRequest
  ): Promise<SetNetworkModeResponse> {
    return this.request("POST", `/api/agents/${encodeURIComponent(id)}/network-mode`, {
      body,
    });
  }

  getAgentLogs(id: string, query?: AgentLogsQuery): Promise<AgentLogsResponse> {
    return this.request("GET", `/api/agents/${encodeURIComponent(id)}/logs`, { query });
  }

  listAgentEvents(id: string, query?: AgentEventsQuery): Promise<ListEventsResponse> {
    return this.request("GET", `/api/agents/${encodeURIComponent(id)}/events`, { query });
  }

  // ── Alerts ───────────────────────────────────────────────────────────

  listAlerts(query?: ListAlertsQuery): Promise<ListAlertsResponse> {
    return this.request("GET", "/api/alerts", { query });
  }

  // ── Applications / Categories ───────────────────────────────────────

  listApplications(query?: ListApplicationsQuery): Promise<ListApplicationsResponse> {
    return this.request("GET", "/api/applications", { query });
  }

  getApplication(id: number): Promise<GetApplicationResponse> {
    return this.request("GET", `/api/applications/${id}`);
  }

  updateApplication(
    id: number,
    body: UpdateApplicationRequest
  ): Promise<UpdateApplicationResponse> {
    return this.request("PATCH", `/api/applications/${id}`, { body });
  }

  listCategories(): Promise<ListCategoriesResponse> {
    return this.request("GET", "/api/categories");
  }

  // ── Events (cross-agent) ─────────────────────────────────────────────

  listEvents(query?: ListEventsQuery): Promise<ListEventsResponse> {
    return this.request("GET", "/api/events", { query });
  }

  // ── Policy Rules ─────────────────────────────────────────────────────

  listPolicyRules(): Promise<ListPolicyRulesResponse> {
    return this.request("GET", "/api/policy-rules");
  }

  createPolicyRule(body: CreatePolicyRuleRequest): Promise<CreatePolicyRuleResponse> {
    return this.request("POST", "/api/policy-rules", { body });
  }

  updatePolicyRule(
    id: number,
    body: UpdatePolicyRuleRequest
  ): Promise<UpdatePolicyRuleResponse> {
    return this.request("PATCH", `/api/policy-rules/${id}`, { body });
  }

  deletePolicyRule(id: number): Promise<DeletePolicyRuleResponse> {
    return this.request("DELETE", `/api/policy-rules/${id}`);
  }

  // ── Settings ─────────────────────────────────────────────────────────

  getSettings(): Promise<GetSettingsResponse> {
    return this.request("GET", "/api/settings");
  }

  updateSettings(body: UpdateSettingsRequest): Promise<UpdateSettingsResponse> {
    return this.request("POST", "/api/settings", { body });
  }

  // ── Audit ────────────────────────────────────────────────────────────

  listAuditLogs(query?: ListAuditLogsQuery): Promise<ListAuditLogsResponse> {
    return this.request("GET", "/api/audit", { query });
  }

  // ── Health ───────────────────────────────────────────────────────────

  getHealth(): Promise<HealthResponse> {
    return this.request("GET", "/api/health");
  }

  getStats(): Promise<StatsResponse> {
    return this.request("GET", "/api/stats");
  }

  // ── Deploy ───────────────────────────────────────────────────────────

  listDeployJobs(): Promise<ListDeployJobsResponse> {
    return this.request("GET", "/api/deploy");
  }

  createDeployJob(body: CreateDeployJobRequest): Promise<CreateDeployJobResponse> {
    return this.request("POST", "/api/deploy", { body });
  }

  getDeployJob(id: string): Promise<GetDeployJobResponse> {
    return this.request("GET", `/api/deploy/${encodeURIComponent(id)}`);
  }

  cancelDeployJob(id: string): Promise<CancelDeployJobResponse> {
    return this.request("DELETE", `/api/deploy/${encodeURIComponent(id)}`);
  }

  uploadFile(file: Buffer | Blob, filename: string): Promise<UploadResponse> {
    const blob = file instanceof Blob ? file : new Blob([file]);
    const form = new FormData();
    form.append("file", blob, filename);
    return this.request("POST", "/api/upload", { formData: form });
  }

  // ── Notifications ───────────────────────────────────────────────────

  testTelegram(): Promise<TestTelegramResponse> {
    return this.request("POST", "/api/test/telegram");
  }

  testEmail(): Promise<TestEmailResponse> {
    return this.request("POST", "/api/test/email");
  }

  // ── Logs ─────────────────────────────────────────────────────────────

  tailServerLogs(query?: TailLogsQuery): Promise<TailLogsResponse> {
    return this.request("GET", "/api/logs", { query });
  }

  // ── Clients v1 ───────────────────────────────────────────────────────

  listClientsV1(): Promise<ListClientsResponse> {
    return this.request("GET", "/api/v1/clients");
  }

  // ── Public file download ─────────────────────────────────────────────

  downloadFile(filename: string): Promise<ArrayBuffer> {
    return this.requestBinary(`/api/file/${encodeURIComponent(filename)}`, false);
  }
}
