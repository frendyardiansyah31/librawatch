import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function clientsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_clients_v1",
      description:
        "List the slim, read-only client projection (id, hostname, ip, mac_address, os, status, floor, last_seen) used by external consumers.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.listClientsV1(),
    }),
  ];
}
