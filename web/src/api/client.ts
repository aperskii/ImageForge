import type { Job, Problem, TokenResponse, TransformationSpec } from "./types";

/** The client identity this demo app presents to /auth/token. */
const CLIENT_ID = "imageforge-web";

/**
 * An error carrying the RFC 7807 problem the API returned.
 *
 * `type` is the stable part and is what code should switch on; `detail` is
 * prose and may be reworded. `requestId` also appears in the server log, so
 * surfacing it turns a user's screenshot into something traceable.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly type: string;
  readonly title: string;
  readonly requestId: string | undefined;
  readonly retryAfter: number | undefined;

  constructor(problem: Problem) {
    super(problem.detail || problem.title || `The server responded with ${problem.status}.`);
    this.name = "ApiError";
    this.status = problem.status;
    this.type = problem.type;
    this.title = problem.title;
    this.requestId = problem.request_id;
    this.retryAfter = problem.retry_after;
  }

  /** True when retrying the same request might work. */
  get isRetryable(): boolean {
    return this.status >= 500 || this.status === 408;
  }

  /** True when the token was missing or is no longer valid. */
  get isUnauthorized(): boolean {
    return this.status === 401;
  }

  /** True when the client is over its rate limit. */
  get isRateLimited(): boolean {
    return this.status === 429;
  }
}

/** Reads a problem response, falling back to the status when the body is not one. */
async function toApiError(response: Response): Promise<ApiError> {
  const fallback: Problem = {
    type: "about:blank",
    title: response.statusText || "Request failed",
    status: response.status,
    detail: `The server responded with ${response.status}.`,
  };

  const headerRetry = Number(response.headers.get("Retry-After"));
  if (Number.isFinite(headerRetry) && headerRetry > 0) {
    fallback.retry_after = headerRetry;
  }
  const headerRequestId = response.headers.get("X-Request-Id");
  if (headerRequestId) {
    fallback.request_id = headerRequestId;
  }

  try {
    const body = (await response.json()) as Partial<Problem>;
    return new ApiError({ ...fallback, ...body, status: body.status ?? response.status });
  } catch {
    // A proxy or a crash can answer with something that is not problem+json.
    return new ApiError(fallback);
  }
}

/** Parses a successful JSON response. */
async function toJson<T>(response: Response): Promise<T> {
  if (!response.ok) {
    throw await toApiError(response);
  }
  return (await response.json()) as T;
}

/**
 * The bearer token for this browser session.
 *
 * It is held in memory rather than localStorage: a credential in storage is
 * readable by any script that gets injected into the page, and this one is
 * cheap to replace because /auth/token will mint another.
 */
let cachedToken: string | null = null;
let inFlight: Promise<string> | null = null;

/** Fetches a token, reusing an in-flight request rather than racing several. */
async function fetchToken(): Promise<string> {
  const response = await fetch("/auth/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ client_id: CLIENT_ID }),
  });

  const issued = await toJson<TokenResponse>(response);
  cachedToken = issued.access_token;
  return issued.access_token;
}

/** Returns a usable token, fetching one if there is none. */
export async function getToken(): Promise<string> {
  if (cachedToken) return cachedToken;

  // Several requests can start at once on first load; they should share one
  // token request rather than each minting their own.
  inFlight ??= fetchToken().finally(() => {
    inFlight = null;
  });
  return inFlight;
}

/** Discards the cached token, so the next request fetches a fresh one. */
export function clearToken(): void {
  cachedToken = null;
}

/**
 * Performs an authenticated request, retrying once with a new token if the
 * server says the current one is no longer good.
 *
 * A token expires after an hour and the server may have restarted with a new
 * signing key, so a single silent retry is the difference between a working app
 * and one that fails until reloaded.
 */
async function authorized(path: string, init: RequestInit, retry = true): Promise<Response> {
  const token = await getToken();
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${token}`);

  const response = await fetch(path, { ...init, headers });
  if (response.status === 401 && retry) {
    clearToken();
    return authorized(path, init, false);
  }
  return response;
}

/**
 * Uploads an image and queues its transformation.
 *
 * Returns the accepted job, which starts out pending: the work has been
 * accepted at this point, not performed.
 */
export async function createJob(
  file: File,
  spec: TransformationSpec,
  signal?: AbortSignal,
): Promise<Job> {
  const body = new FormData();
  body.append("file", file);
  body.append("spec", JSON.stringify(spec));

  const response = await authorized("/uploads", {
    method: "POST",
    body,
    ...(signal ? { signal } : {}),
  });
  return toJson<Job>(response);
}

/** Fetches the current state of a job. */
export async function getJob(id: string, signal?: AbortSignal): Promise<Job> {
  const response = await authorized(`/jobs/${encodeURIComponent(id)}`, {
    ...(signal ? { signal } : {}),
  });
  return toJson<Job>(response);
}
