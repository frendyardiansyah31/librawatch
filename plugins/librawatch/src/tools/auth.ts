import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function authTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_login",
      description: "Log in to the LibraWatch server and obtain a session token.",
      inputSchema: {
        type: "object",
        properties: {
          username: { type: "string" },
          password: { type: "string" },
        },
        required: ["username", "password"],
      },
      handler: (input: { username: string; password: string }) =>
        client.login({ username: input.username, password: input.password }),
    }),

    defineTool({
      name: "librawatch_logout",
      description: "Log out of the current LibraWatch session.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.logout(),
    }),
  ];
}
