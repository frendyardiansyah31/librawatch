import type { LibraWatchClient } from "../generated/client.js";
import type { PolicyAction, PolicyAppStatus, PolicyExecutionLocation } from "../generated/models.js";
import { defineTool, type Tool } from "./types.js";

const policyRuleInputSchema = {
  type: "object" as const,
  properties: {
    name: { type: "string" },
    event_type: { type: "string" },
    category_id: { type: "number" },
    app_status: {
      type: "string",
      enum: ["", "pending_review", "allowed", "blocked", "ignored"],
    },
    file_extension: { type: "string" },
    execution_location: {
      type: "string",
      enum: ["", "downloads", "desktop", "temp", "usb"],
    },
    device_group: { type: "string" },
    action: { type: "string", enum: ["log", "notify", "block", "delete", "kill"] },
    enabled: { type: "boolean" },
  },
  required: ["name", "action"],
};

// `type`, not `interface` — a Tool<TInput,...> must structurally satisfy
// Tool<Record<string, unknown>, unknown> (the Tool[] element default), and
// interfaces (unlike type aliases) don't get the implicit index signature
// that assignment requires.
type PolicyRuleFields = {
  name: string;
  event_type?: string;
  category_id?: number;
  app_status?: PolicyAppStatus;
  file_extension?: string;
  execution_location?: PolicyExecutionLocation;
  device_group?: string;
  action: PolicyAction;
  enabled?: boolean;
};

export function policyRulesTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_policy_rules",
      description: "List all Policy Engine rules (enabled and disabled).",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.listPolicyRules(),
    }),

    defineTool({
      name: "librawatch_create_policy_rule",
      description:
        "Create a new Policy Engine rule. Filter fields left empty mean 'applies to all'.",
      inputSchema: policyRuleInputSchema,
      handler: (input: PolicyRuleFields) => client.createPolicyRule(input),
    }),

    defineTool({
      name: "librawatch_update_policy_rule",
      description: "Update an existing Policy Engine rule.",
      inputSchema: {
        ...policyRuleInputSchema,
        properties: { id: { type: "number" }, ...policyRuleInputSchema.properties },
        required: ["id", ...policyRuleInputSchema.required],
      },
      handler: (input: PolicyRuleFields & { id: number }) =>
        client.updatePolicyRule(input.id, input),
    }),

    defineTool({
      name: "librawatch_delete_policy_rule",
      description: "Delete a Policy Engine rule.",
      inputSchema: {
        type: "object",
        properties: { id: { type: "number" } },
        required: ["id"],
      },
      handler: (input: { id: number }) => client.deletePolicyRule(input.id),
    }),
  ];
}
