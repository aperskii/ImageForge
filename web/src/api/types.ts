/**
 * The wire shape of the Go API. These mirror internal/adapters/httpapi; if the
 * server's JSON changes, it changes here too.
 */

/** Output encodings the API accepts. */
export const FORMATS = ["jpeg", "png", "webp", "avif"] as const;
export type Format = (typeof FORMATS)[number];

/** Formats the server can only encode with the libvips backend. */
export const LIBVIPS_ONLY_FORMATS: readonly Format[] = ["webp", "avif"];

/** PNG is lossless, so the API rejects a quality on it. */
export function isLossless(format: Format): boolean {
  return format === "png";
}

/** The largest width or height the API accepts, from domain.MaxDimension. */
export const MAX_DIMENSION = 10000;

/** Lifecycle states a job moves through. */
export type JobStatus = "pending" | "processing" | "done" | "failed";

/** A job in a state that will not change again. */
export function isTerminal(status: JobStatus): boolean {
  return status === "done" || status === "failed";
}

/** The transformation requested for a job. */
export interface TransformationSpec {
  width: number;
  height: number;
  format: Format;
  quality: number;
  watermark: boolean;
  strip_metadata: boolean;
}

/** A job as the API reports it. */
export interface Job {
  id: string;
  status: JobStatus;
  transformation: TransformationSpec;
  created_at: string;
  updated_at: string;
  /** Storage key of the result, set once the job is done. */
  result_key?: string;
  /** Where the result can be fetched, set only when the server knows a public base URL. */
  result_url?: string;
  /** Failure reason, set only when the job failed. */
  error?: string;
}

/**
 * An RFC 7807 problem detail, the body of every non-2xx response.
 *
 * `type` is the stable identifier to switch on; `title` and `detail` are prose.
 * `request_id` and `retry_after` are extension members the API adds.
 */
export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
  instance?: string;
  request_id?: string;
  retry_after?: number;
}

/** Problem types this app treats specially. */
export const PROBLEM_BASE = "https://imageforge.dev/problems/";
export const PROBLEM_RATE_LIMITED = `${PROBLEM_BASE}rate-limited`;
export const PROBLEM_UNSUPPORTED_MEDIA = `${PROBLEM_BASE}unsupported-media-type`;
export const PROBLEM_PAYLOAD_TOO_LARGE = `${PROBLEM_BASE}payload-too-large`;

/** The body of a successful POST /auth/token. */
export interface TokenResponse {
  access_token: string;
  token_type: string;
  expires_in: number;
}
