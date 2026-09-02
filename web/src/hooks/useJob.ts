import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, createJob, getJob } from "../api/client";
import type { Job, TransformationSpec } from "../api/types";
import { isTerminal } from "../api/types";

/** How often a job in flight is polled. */
export const POLL_INTERVAL_MS = 1500;

/** Give up on a job that never settles, rather than polling for ever. */
export const POLL_TIMEOUT_MS = 5 * 60 * 1000;

/** Query key for a single job. */
export function jobKey(id: string): readonly unknown[] {
  return ["job", id];
}

/**
 * Watches one job, polling until it reaches a terminal state.
 *
 * Passing null disables the query, which is how the caller says "nothing is in
 * flight yet" without breaking the rules of hooks.
 */
export function useJob(id: string | null) {
  return useQuery<Job, ApiError>({
    queryKey: jobKey(id ?? ""),
    queryFn: ({ signal }) => getJob(id as string, signal),
    enabled: id !== null,
    // Polling stops the moment the job settles, so a finished job costs
    // nothing to keep on screen.
    refetchInterval: (query) => {
      const job = query.state.data;
      if (job && isTerminal(job.status)) {
        return false;
      }
      if (Date.now() - query.state.dataUpdatedAt > POLL_TIMEOUT_MS) {
        return false;
      }
      return POLL_INTERVAL_MS;
    },
    // Keep polling even when the tab is in the background. A job takes
    // seconds, and pausing here would leave someone who switched tabs looking
    // at a stale spinner when they came back.
    refetchIntervalInBackground: true,
    // Refetch on focus only while the job is still moving; one that has
    // settled will never change again.
    refetchOnWindowFocus: (query) => !(query.state.data && isTerminal(query.state.data.status)),
    // A 404 means the job does not exist and never will; retrying only delays
    // telling the user.
    retry: (failureCount, error) => error.isRetryable && failureCount < 3,
    staleTime: 0,
  });
}

/** Uploads an image and seeds the cache with the accepted job. */
export function useCreateJob() {
  const queryClient = useQueryClient();

  return useMutation<Job, ApiError, { file: File; spec: TransformationSpec }>({
    mutationFn: ({ file, spec }) => createJob(file, spec),
    onSuccess: (job) => {
      // Seed the cache so the first poll has something to show immediately
      // rather than a spinner for one interval.
      queryClient.setQueryData(jobKey(job.id), job);
    },
  });
}
