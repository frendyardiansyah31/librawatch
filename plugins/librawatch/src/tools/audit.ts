import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function auditTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_audit_logs",
      description: "List the audit log (who did what: kill process, delete agent, deploy, upload, etc.).",
      inputSchema: {
        type: "object",
        properties: { limit: { type: "number" } },
      },
      handler: (input: { limit?: number }) => client.listAuditLogs({ limit: input.limit }),
    }),
  ];
}
