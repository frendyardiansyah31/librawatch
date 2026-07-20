import type { LibraWatchClient } from "../generated/client.js";
import type { ApplicationStatus } from "../generated/models.js";
import { defineTool, type Tool } from "./types.js";

export function applicationsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_applications",
      description: "List the application catalog, optionally filtered by status and/or category.",
      inputSchema: {
        type: "object",
        properties: {
          status: {
            type: "string",
            enum: ["pending_review", "allowed", "blocked", "ignored"],
          },
          category_id: { type: "number" },
        },
      },
      handler: (input: { status?: ApplicationStatus; category_id?: number }) =>
        client.listApplications({ status: input.status, category_id: input.category_id }),
    }),

    defineTool({
      name: "librawatch_get_application",
      description: "Get details for one cataloged application, including per-device sightings.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "number" } },
        required: ["id"],
      },
      handler: (input: { id: number }) => client.getApplication(input.id),
    }),

    defineTool({
      name: "librawatch_update_application",
      description: "Update an application's status and/or category in the catalog.",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "number" },
          status: {
            type: "string",
            enum: ["pending_review", "allowed", "blocked", "ignored"],
          },
          category_id: { type: "number" },
        },
        required: ["id", "status"],
      },
      handler: (input: { id: number; status: ApplicationStatus; category_id?: number }) =>
        client.updateApplication(input.id, {
          status: input.status,
          category_id: input.category_id,
        }),
    }),
  ];
}
