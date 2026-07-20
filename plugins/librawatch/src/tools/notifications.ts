import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function notificationsTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_test_telegram",
      description: "Send a test message via the configured Telegram bot.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.testTelegram(),
    }),

    defineTool({
      name: "librawatch_test_email",
      description: "Send a test email via the configured SMTP settings.",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.testEmail(),
    }),
  ];
}
