import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

export function categoriesTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_list_categories",
      description: "List application categories (Browser, Office, Programming, Games, etc.).",
      inputSchema: { type: "object", properties: {} },
      handler: () => client.listCategories(),
    }),
  ];
}
