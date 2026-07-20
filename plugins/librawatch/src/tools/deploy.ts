import type { LibraWatchClient } from "../generated/client.js";
import type { DeployType } from "../generated/models.js";
import { defineTool, type Tool } from "./types.js";

export function deployTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_deploy_jobs",
      description: "List all deploy jobs (most recent 100).",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.listDeployJobs(),
    }),

    defineTool({
      name: "librawatch_create_deploy_job",
      description:
        "Create a deploy job targeting one or more PCs. type: exec (PowerShell), winget (install/uninstall --id <PackageID>), file_deploy (run an uploaded file), deepfreeze (thaw/freeze/query_df), or install_ssh.",
      inputSchema: {
        type: "object",
        properties: {
          type: {
            type: "string",
            enum: ["exec", "winget", "file_deploy", "deepfreeze", "install_ssh"],
          },
          payload: { type: "string" },
          args: { type: "string" },
          targets: { type: "array", items: { type: "string" } },
          priority: { type: "number" },
          expire_at: { type: "string" },
          max_retry: { type: "number" },
        },
        required: ["type", "targets"],
      },
      handler: (input: {
        type: DeployType;
        payload?: string;
        args?: string;
        targets: string[];
        priority?: number;
        expire_at?: string;
        max_retry?: number;
      }) =>
        client.createDeployJob({
          type: input.type,
          payload: input.payload,
          args: input.args,
          targets: input.targets,
          priority: input.priority,
          expire_at: input.expire_at,
          max_retry: input.max_retry,
        }),
    }),

    defineTool({
      name: "librawatch_get_deploy_job",
      description: "Get details for one deploy job, including per-PC results.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
      handler: (input: { id: string }) => client.getDeployJob(input.id),
    }),

    defineTool({
      name: "librawatch_cancel_deploy_job",
      description: "Cancel a pending or in-flight deploy job.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
      handler: (input: { id: string }) => client.cancelDeployJob(input.id),
    }),
  ];
}
