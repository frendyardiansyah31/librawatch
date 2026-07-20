import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function alertsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_alerts",
      description:
        "List recent alerts (cpu_high, ram_high, blacklisted_app, offline, recovery, peripheral_removed).",
      inputSchema: {
        type: "object",
        properties: { limit: { type: "number" } },
      },
      handler: (input: { limit?: number }) => client.listAlerts({ limit: input.limit }),
    }),
  ];
}
