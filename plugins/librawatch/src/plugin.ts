import { loadConfig, type LibraWatchConfig } from "./config.js";
import { LibraWatchClient } from "./generated/client.js";
import type { Tool } from "./tools/types.js";
import { authTools } from "./tools/auth.js";
import { agentsTools } from "./tools/agents.js";
import { alertsTools } from "./tools/alerts.js";
import { applicationsTools } from "./tools/applications.js";
import { categoriesTools } from "./tools/categories.js";
import { eventsTools } from "./tools/events.js";
import { policyRulesTools } from "./tools/policyRules.js";
import { settingsTools } from "./tools/settings.js";
import { auditTools } from "./tools/audit.js";
import { healthTools } from "./tools/health.js";
import { deployTools } from "./tools/deploy.js";
import { notificationsTools } from "./tools/notifications.js";
import { logsTools } from "./tools/logs.js";
import { clientsTools } from "./tools/clients.js";
import { filesTools } from "./tools/files.js";

export interface LibraWatchPlugin {
  name: string;
  version: string;
  tools: Tool[];
}

const TOOL_FACTORIES = [
  authTools,
  agentsTools,
  alertsTools,
  applicationsTools,
  categoriesTools,
  eventsTools,
  policyRulesTools,
  settingsTools,
  auditTools,
  healthTools,
  deployTools,
  notificationsTools,
  logsTools,
  clientsTools,
  filesTools,
];

/**
 * Assembles the LibraWatch OpenClaw plugin: one LibraWatchClient plus every
 * tool from tools/*.ts. The returned shape ({name, version, tools}) is
 * minimal and adjustable — no OpenClaw host-side plugin-loader contract
 * exists in this repo to conform to (see README "How this plugin is
 * structured"). Pass a config explicitly (e.g. in tests); defaults to
 * reading LIBRAWATCH_URL / LIBRAWATCH_API_KEY from the environment.
 */
export function createLibraWatchPlugin(config: LibraWatchConfig = loadConfig()): LibraWatchPlugin {
  const client = new LibraWatchClient(config);
  const tools = TOOL_FACTORIES.flatMap((factory) => factory(client));
  return { name: "librawatch", version: "0.1.0", tools };
}

export default createLibraWatchPlugin;
