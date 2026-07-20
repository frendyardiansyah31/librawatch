import { defineToolPlugin } from "openclaw/plugin-sdk/tool-plugin";
import type { TSchema } from "typebox";
import createLibraWatchPlugin from "./plugin.js";

/**
 * Bridges the existing REST-adapter plugin (plugin.ts, tools/*.ts — unchanged)
 * into OpenClaw's real plugin contract. Built against the actual SDK found at
 * openclaw/plugin-sdk/tool-plugin (see node_modules/openclaw), not guessed.
 * Config is read once from LIBRAWATCH_URL / LIBRAWATCH_API_KEY at plugin load,
 * same as running the adapter standalone.
 */
const inner = createLibraWatchPlugin();

export default defineToolPlugin({
  id: "librawatch",
  name: "LibraWatch",
  description:
    "LibraWatch REST API adapter — monitor and manage library lab PC agents, " +
    "alerts, deploy jobs, and policy rules via the LibraWatch Go server.",
  tools: (tool) =>
    inner.tools.map((t) =>
      tool({
        name: t.name,
        description: t.description,
        // t.inputSchema is plain JSON Schema; defineToolPlugin forwards
        // `parameters` untouched (see tool-plugin-*.js), so this is safe
        // without pulling in typebox as a runtime dependency.
        parameters: t.inputSchema as unknown as TSchema,
        execute: async (params: unknown) => t.execute(params as Record<string, unknown>),
      }),
    ),
});
