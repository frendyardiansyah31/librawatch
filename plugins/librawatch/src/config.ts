export interface LibraWatchConfig {
  /** Base URL of the LibraWatch Go server, e.g. "http://localhost:8080". No trailing slash. */
  baseUrl: string;
  /**
   * Static bearer token. Used as-is on every request, NEVER auto-refreshed
   * (there's no way to obtain a replacement without credentials). Takes
   * precedence over username/password if both are set. Prefer
   * username/password below for any long-running deployment — the server's
   * login tokens expire after 8h (server/auth.go's sessionDuration) and
   * this client can only renew them automatically when it holds credentials.
   */
  apiKey?: string;
  /** Login credentials — if set, the client logs in lazily and re-logs in
   *  automatically before expiry and on an unexpected 401. */
  username?: string;
  password?: string;
}

export class LibraWatchConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "LibraWatchConfigError";
  }
}

/**
 * Reads LIBRAWATCH_URL (required) and LIBRAWATCH_API_KEY (optional — the Go
 * server's bearer auth can be disabled entirely by leaving auth.admin_username
 * empty in config.yaml, so a missing key must not throw here).
 */
export function loadConfig(env: NodeJS.ProcessEnv = process.env): LibraWatchConfig {
  const rawUrl = env.LIBRAWATCH_URL?.trim();
  if (!rawUrl) {
    throw new LibraWatchConfigError(
      "LIBRAWATCH_URL is required (e.g. http://localhost:8080)"
    );
  }

  let parsed: URL;
  try {
    parsed = new URL(rawUrl);
  } catch {
    throw new LibraWatchConfigError(
      `LIBRAWATCH_URL is not a valid absolute URL: ${rawUrl}`
    );
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new LibraWatchConfigError(
      `LIBRAWATCH_URL must use http:// or https://, got: ${parsed.protocol}`
    );
  }

  const baseUrl = rawUrl.replace(/\/+$/, "");
  const apiKey = env.LIBRAWATCH_API_KEY?.trim() || undefined;
  const username = env.LIBRAWATCH_USERNAME?.trim() || undefined;
  const password = env.LIBRAWATCH_PASSWORD?.trim() || undefined;

  if (Boolean(username) !== Boolean(password)) {
    throw new LibraWatchConfigError(
      "LIBRAWATCH_USERNAME and LIBRAWATCH_PASSWORD must both be set, or both omitted"
    );
  }

  return { baseUrl, apiKey, username, password };
}
