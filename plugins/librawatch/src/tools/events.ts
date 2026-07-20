import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function eventsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_events",
      description:
        "List Policy Enforcement events across all agents (USB, downloads, config changes, installs, exec_policy, peripherals).",
      inputSchema: {
        type: "object",
        properties: {
          agent_id: { type: "string" },
          type: { type: "string" },
          limit: { type: "number" },
        },
      },
      handler: (input: { agent_id?: string; type?: string; limit?: number }) =>
        client.listEvents({
          agent_id: input.agent_id,
          type: input.type,
          limit: input.limit,
        }),
    }),
  ];
}
