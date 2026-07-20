import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function settingsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_get_settings",
      description: "Get all server settings (thresholds, Telegram, Email, etc.) as key-value pairs.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.getSettings(),
    }),

    defineTool({
      name: "librawatch_update_settings",
      description:
        "Update one or more server settings by key-value pair. Applies immediately, no restart needed.",
      inputSchema: {
        type: "object",
        properties: {},
        additionalProperties: true,
      },
      handler: (input: Record<string, unknown>) => client.updateSettings(input),
    }),
  ];
}
