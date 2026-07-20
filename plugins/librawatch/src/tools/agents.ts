import type { LibraWatchClient } from "../generated/client.js";
import type { NetworkMode } from "../generated/models.js";
import { defineTool, type Tool } from "./types.js";

export function agentsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_agents",
      description: "List all monitored PCs (agents) with their latest metrics.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.listAgents(),
    }),

    defineTool({
      name: "librawatch_get_agent",
      description: "Get details for a single agent (PC) by ID.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
      handler: (input: { id: string }) => client.getAgent(input.id),
    }),

    defineTool({
      name: "librawatch_update_agent",
      description:
        "Update an agent's mesh_id, device_group, and/or floor. Omit a field to leave it unchanged.",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          mesh_id: { type: "string" },
          device_group: { type: "string" },
          floor: { type: "string" },
        },
        required: ["id"],
      },
      handler: (input: {
        id: string;
        mesh_id?: string;
        device_group?: string;
        floor?: string;
      }) =>
        client.updateAgent(input.id, {
          mesh_id: input.mesh_id,
          device_group: input.device_group,
          floor: input.floor,
        }),
    }),

    defineTool({
      name: "librawatch_delete_agent",
      description: "Delete an agent and its associated metrics/processes/alerts from the database.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
      handler: (input: { id: string }) => client.deleteAgent(input.id),
    }),

    defineTool({
      name: "librawatch_get_agent_metrics",
      description: "Get 24-hour CPU/RAM metric history for an agent.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
      handler: (input: { id: string }) => client.listAgentMetrics(input.id),
    }),

    defineTool({
      name: "librawatch_get_agent_processes",
      description: "List currently running processes on an agent.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
      handler: (input: { id: string }) => client.listAgentProcesses(input.id),
    }),

    defineTool({
      name: "librawatch_kill_process",
      description: "Kill a process on an agent, by PID or by name.",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          pid: { type: "number" },
          name: { type: "string" },
        },
        required: ["id"],
      },
      handler: (input: { id: string; pid?: number; name?: string }) =>
        client.killProcess(input.id, { pid: input.pid, name: input.name }),
    }),

    defineTool({
      name: "librawatch_set_agent_network_mode",
      description:
        "Set the desired network mode (ethernet, wifi, or both) for an agent. Persists immediately and applies live if the agent is online.",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          mode: { type: "string", enum: ["ethernet", "wifi", "both"] },
        },
        required: ["id", "mode"],
      },
      handler: (input: { id: string; mode: NetworkMode }) =>
        client.setAgentNetworkMode(input.id, { mode: input.mode }),
    }),

    defineTool({
      name: "librawatch_get_agent_logs",
      description: "Fetch the agent.log content from a PC via the WebSocket relay.",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          lines: { type: "number" },
        },
        required: ["id"],
      },
      handler: (input: { id: string; lines?: number }) =>
        client.getAgentLogs(input.id, { lines: input.lines }),
    }),

    defineTool({
      name: "librawatch_get_agent_events",
      description: "Get the event timeline (USB, downloads, config changes, etc.) for one agent.",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          type: { type: "string" },
          limit: { type: "number" },
        },
        required: ["id"],
      },
      handler: (input: { id: string; type?: string; limit?: number }) =>
        client.listAgentEvents(input.id, { type: input.type, limit: input.limit }),
    }),
  ];
}
