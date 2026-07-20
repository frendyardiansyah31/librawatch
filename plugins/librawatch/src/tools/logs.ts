import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function logsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_tail_server_logs",
      description: "Tail the LibraWatch server's own log file (logs/server.log).",
      inputSchema: {
        type: "object",
        properties: { lines: { type: "number" } },
      },
      handler: (input: { lines?: number }) => client.tailServerLogs({ lines: input.lines }),
    }),
  ];
}
