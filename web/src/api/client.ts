import type { ApiErrorBody, Job, TransformationSpec } from "./types";

/**
 * An error carrying what the API said about it.
 *
 * The server returns a stable `code` and a `request_id` that also appears in
 * its logs, so surfacing both turns a user's screenshot into something that can
 * actually be traced.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;

  constructor(status: number, code: string, message: string, requestId?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }

  /** True when retrying the same request might work. */
  get isRetryable(): boolean {
    return this.status >= 500 || this.status === 408 || this.status === 429;
  }
}

/** Reads an error response, falling back to the status when the body is not ours. */
async function toApiError(response: Response): Promise<ApiError> {
  let code = "unknown";
  let message = `The server responded with ${response.status}.`;
  let requestId = response.headers.get("X-Request-Id") ?? undefined;

  try {
    const body = (await response.json()) as Partial<ApiErrorBody>;
    if (body.error) {
      code = body.error.code || code;
      message = body.error.message || message;
      requestId = body.error.request_id ?? requestId;
    }
  } catch {
    // A proxy or a crash can answer with something that is not our JSON. The
    // status alone is still worth reporting.
  }

  return new ApiError(response.status, code, message, requestId);
}

/** Parses a successful JSON response. */
async function toJson<T>(response: Response): Promise<T> {
  if (!response.ok) {
    throw await toApiError(response);
  }
  return (await response.json()) as T;
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

  const response = await fetch("/uploads", {
    method: "POST",
    body,
    ...(signal ? { signal } : {}),
  });
  return toJson<Job>(response);
}

/** Fetches the current state of a job. */
export async function getJob(id: string, signal?: AbortSignal): Promise<Job> {
  const response = await fetch(`/jobs/${encodeURIComponent(id)}`, {
    ...(signal ? { signal } : {}),
  });
  return toJson<Job>(response);
}
