import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function healthTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_get_health",
      description: "Get server status: uptime and number of agents online.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.getHealth(),
    }),

    defineTool({
      name: "librawatch_get_stats",
      description: "Get a summary: agents online and alerts triggered today.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.getStats(),
    }),
  ];
}
