import type { ApiError } from "../api/client";
import type { Job } from "../api/types";
import { CompareSlider } from "./CompareSlider";
import { POLL_INTERVAL_MS } from "../hooks/useJob";

interface JobResultProps {
  job: Job | undefined;
  error: ApiError | null;
  isLoading: boolean;
  /** Object URL of the file the user chose, used as the "before" image. */
  originalUrl: string | null;
  onReset: () => void;
}

/** A short human description of where a job has got to. */
function statusLabel(status: Job["status"]): string {
  switch (status) {
    case "pending":
      return "Queued, waiting for a worker";
    case "processing":
      return "Processing";
    case "done":
      return "Done";
    case "failed":
      return "Failed";
  }
}

const badgeClass: Record<Job["status"], string> = {
  pending: "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300",
  processing: "bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300",
  done: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  failed: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
};

export function StatusBadge({ status }: { status: Job["status"] }) {
  const inFlight = status === "pending" || status === "processing";
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium ${badgeClass[status]}`}
    >
      {inFlight && (
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" aria-hidden="true" />
      )}
      {statusLabel(status)}
    </span>
  );
}

export function JobResult({ job, error, isLoading, originalUrl, onReset }: JobResultProps) {
  if (error) {
    return (
      <section className="rounded-xl border border-red-200 bg-red-50 p-6 dark:border-red-900 dark:bg-red-950/40">
        <h2 className="font-semibold text-red-900 dark:text-red-200">Could not read the job</h2>
        <p className="mt-1 text-sm text-red-800 dark:text-red-300">{error.message}</p>
        {error.requestId && (
          <p className="mt-2 font-mono text-xs text-red-700 dark:text-red-400">
            Request ID: {error.requestId}
          </p>
        )}
        <button
          type="button"
          onClick={onReset}
          className="mt-4 rounded-md bg-red-600 px-3 py-1.5 text-sm font-medium text-white transition hover:bg-red-700"
        >
          Start over
        </button>
      </section>
    );
  }

  if (isLoading || !job) {
    return (
      <section className="rounded-xl border border-slate-200 bg-white p-6 dark:border-slate-800 dark:bg-slate-900">
        <div className="animate-pulse space-y-3">
          <div className="h-4 w-1/3 rounded bg-slate-200 dark:bg-slate-800" />
          <div className="h-48 rounded bg-slate-200 dark:bg-slate-800" />
        </div>
      </section>
    );
  }

  const inFlight = job.status === "pending" || job.status === "processing";

  return (
    <section className="rounded-xl border border-slate-200 bg-white p-6 dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold text-slate-900 dark:text-slate-100">Job</h2>
          <p className="font-mono text-xs text-slate-500 dark:text-slate-400">{job.id}</p>
        </div>
        <StatusBadge status={job.status} />
      </div>

      {inFlight && (
        <div
          className="mt-4 rounded-lg bg-slate-50 p-4 text-sm text-slate-600 dark:bg-slate-800/50 dark:text-slate-300"
          role="status"
          aria-live="polite"
        >
          {statusLabel(job.status)}. Checking again every {POLL_INTERVAL_MS / 1000}s.
        </div>
      )}

      {job.status === "failed" && (
        <div className="mt-4 rounded-lg bg-red-50 p-4 dark:bg-red-950/40" role="alert">
          <p className="text-sm font-medium text-red-900 dark:text-red-200">
            The transformation failed.
          </p>
          {job.error && (
            <p className="mt-1 font-mono text-xs break-words text-red-800 dark:text-red-300">
              {job.error}
            </p>
          )}
        </div>
      )}

      {job.status === "done" && (
        <div className="mt-4 space-y-4">
          {job.result_url && originalUrl ? (
            <CompareSlider beforeUrl={originalUrl} afterUrl={job.result_url} />
          ) : (
            <div className="rounded-lg bg-amber-50 p-4 text-sm dark:bg-amber-950/30">
              <p className="font-medium text-amber-900 dark:text-amber-200">
                The result is ready, but this server does not publish a URL for it.
              </p>
              <p className="mt-1 text-amber-800 dark:text-amber-300">
                Set <code className="font-mono">IMAGEFORGE_PUBLIC_BASE_URL</code> on the API so it
                can return a <code className="font-mono">result_url</code>, and the comparison will
                appear here.
              </p>
              {job.result_key && (
                <p className="mt-2 font-mono text-xs text-amber-700 dark:text-amber-400">
                  Stored at {job.result_key}
                </p>
              )}
            </div>
          )}

          <dl className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
            <div>
              <dt className="text-slate-500 dark:text-slate-400">Format</dt>
              <dd className="font-medium text-slate-900 dark:text-slate-100">
                {job.transformation.format.toUpperCase()}
              </dd>
            </div>
            <div>
              <dt className="text-slate-500 dark:text-slate-400">Width</dt>
              <dd className="font-medium text-slate-900 dark:text-slate-100">
                {job.transformation.width || "auto"}
              </dd>
            </div>
            <div>
              <dt className="text-slate-500 dark:text-slate-400">Height</dt>
              <dd className="font-medium text-slate-900 dark:text-slate-100">
                {job.transformation.height || "auto"}
              </dd>
            </div>
            <div>
              <dt className="text-slate-500 dark:text-slate-400">Quality</dt>
              <dd className="font-medium text-slate-900 dark:text-slate-100">
                {job.transformation.quality || "default"}
              </dd>
            </div>
          </dl>

          {job.result_url && (
            <a
              href={job.result_url}
              target="_blank"
              rel="noreferrer"
              className="inline-block rounded-md bg-slate-900 px-3 py-1.5 text-sm font-medium text-white transition hover:bg-slate-700 dark:bg-slate-100 dark:text-slate-900 dark:hover:bg-white"
            >
              Open the full result
            </a>
          )}
        </div>
      )}

      <button
        type="button"
        onClick={onReset}
        className="mt-6 rounded-md px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-300 transition hover:bg-slate-100 dark:text-slate-200 dark:ring-slate-600 dark:hover:bg-slate-800"
      >
        Transform another image
      </button>
    </section>
  );
}
