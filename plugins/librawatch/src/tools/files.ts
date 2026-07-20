import type { LibraWatchClient } from "../generated/client.js";
import { defineTool, type Tool } from "./types.js";

// Tool inputs/outputs are JSON, so binary content crosses this boundary as
// base64. The generated client itself works with Buffer/Blob/ArrayBuffer —
// this base64 (de)serialization is glue, not business logic, and stays
// confined to this one file.

export function filesTools(client: LibraWatchClient): Tool[] {
  return [
    defineTool({
      name: "librawatch_upload_file",
      description:
        "Upload an installer file (.exe, .msi, .bat, .ps1 only) to be used by a deploy job (type=file_deploy).",
      inputSchema: {
        type: "object",
        properties: {
          filename: { type: "string" },
          contentBase64: { type: "string", description: "File content, base64-encoded" },
        },
        required: ["filename", "contentBase64"],
      },
      handler: async (input: { filename: string; contentBase64: string }) =>
        client.uploadFile(Buffer.from(input.contentBase64, "base64"), input.filename),
    }),

    defineTool({
      name: "librawatch_download_file",
      description: "Download a previously uploaded file by filename, returned as base64.",
      inputSchema: {
        type: "object",
        properties: { filename: { type: "string" } },
        required: ["filename"],
      },
      handler: async (input: { filename: string }) => {
        const bytes = await client.downloadFile(input.filename);
        return {
          filename: input.filename,
          contentBase64: Buffer.from(bytes).toString("base64"),
          byteLength: bytes.byteLength,
        };
      },
    }),
  ];
}
