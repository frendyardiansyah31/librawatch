import { LibraWatchApiError, LibraWatchNetworkError } from "../generated/client.js";

export interface JsonSchema {
  type: "object";
  properties: Record<string, unknown>;
  required?: string[];
  additionalProperties?: boolean;
}

export interface ToolError {
  status?: number;
  message: string;
}

export type ToolOutcome<T> =
  | { ok: true; data: T }
  | { ok: false; error: ToolError };

/**
 * Minimal, adjustable shape — no OpenClaw host-side plugin contract exists
 * anywhere in this repo to conform to, so this is our own design. If the
 * real OpenClaw host expects a different tool shape, adapt this interface
 * (and defineTool below) — nothing else in tools/*.ts should need to change.
 */
export interface Tool<TInput = Record<string, unknown>, TOutput = unknown> {
  name: string;
  description: string;
  inputSchema: JsonSchema;
  execute(input: TInput): Promise<ToolOutcome<TOutput>>;
}

/** Single place that turns any thrown error into a meaningful ToolError. */
export function toToolError(err: unknown): ToolError {
  if (err instanceof LibraWatchApiError) {
    return { status: err.status, message: err.message };
  }
  if (err instanceof LibraWatchNetworkError) {
    return { message: err.message };
  }
  if (err instanceof Error) {
    return { message: err.message };
  }
  return { message: "Unexpected error" };
}

/**
 * Wraps a handler that calls the LibraWatchClient (and may throw) into a
 * Tool whose execute() never throws — every tool file builds its tools with
 * this instead of repeating try/catch + toToolError itself (DRY).
 */
export function defineTool<TInput, TOutput>(def: {
  name: string;
  description: string;
  inputSchema: JsonSchema;
  handler: (input: TInput) => Promise<TOutput>;
}): Tool<TInput, TOutput> {
  return {
    name: def.name,
    description: def.description,
    inputSchema: def.inputSchema,
    async execute(input: TInput): Promise<ToolOutcome<TOutput>> {
      try {
        const data = await def.handler(input);
        return { ok: true, data };
      } catch (err) {
        return { ok: false, error: toToolError(err) };
      }
    },
  };
}
