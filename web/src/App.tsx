import { useEffect, useState } from "react";
import { Dropzone } from "./components/Dropzone";
import { Gallery } from "./components/Gallery";
import { JobResult } from "./components/JobResult";
import { INITIAL_FORM, TransformForm, toSpec, validate } from "./components/TransformForm";
import type { FormState } from "./components/TransformForm";
import { useCreateJob, useJob } from "./hooks/useJob";
import { makeThumbnail, useGallery } from "./hooks/useGallery";
import { isTerminal } from "./api/types";

export default function App() {
  const [file, setFile] = useState<File | null>(null);
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [form, setForm] = useState<FormState>(INITIAL_FORM);
  const [jobId, setJobId] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const gallery = useGallery();
  const createJob = useCreateJob();
  const job = useJob(jobId);

  // The preview is an object URL, which holds the file in memory until it is
  // revoked. Tie its life to the file it came from.
  useEffect(() => {
    if (!file) {
      setPreviewUrl(null);
      return;
    }
    const url = URL.createObjectURL(file);
    setPreviewUrl(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);

  // Keep the gallery entry in step with the job as it progresses.
  useEffect(() => {
    if (!job.data) return;
    gallery.update(job.data.id, {
      status: job.data.status,
      ...(job.data.result_url ? { resultUrl: job.data.result_url } : {}),
      ...(job.data.error ? { error: job.data.error } : {}),
    });
  }, [job.data, gallery]);

  const busy = createJob.isPending || (job.data ? !isTerminal(job.data.status) : jobId !== null);

  async function onSubmit(event: React.FormEvent) {
    event.preventDefault();
    setFormError(null);

    if (!file) {
      setFormError("Choose an image first.");
      return;
    }
    const invalid = validate(form);
    if (invalid) {
      setFormError(invalid);
      return;
    }

    const spec = toSpec(form);
    try {
      const created = await createJob.mutateAsync({ file, spec });
      setJobId(created.id);

      // The thumbnail is rendered from the local file, so history survives even
      // when results are not publicly readable.
      const thumbnail = await makeThumbnail(file).catch(() => "");
      gallery.add({
        id: created.id,
        status: created.status,
        fileName: file.name,
        createdAt: created.created_at,
        thumbnail,
        width: spec.width,
        height: spec.height,
        format: spec.format,
      });
    } catch {
      // The mutation's own error state renders this; nothing to add here.
    }
  }

  function reset() {
    setJobId(null);
    setFile(null);
    setFormError(null);
    createJob.reset();
  }

  return (
    <div className="min-h-full bg-slate-50 text-slate-900 dark:bg-slate-950 dark:text-slate-100">
      <header className="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <div className="mx-auto flex max-w-5xl items-center justify-between px-4 py-4 sm:px-6">
          <div>
            <h1 className="text-lg font-semibold">ImageForge</h1>
            <p className="text-sm text-slate-500 dark:text-slate-400">
              Upload an image, pick a transformation, watch it process.
            </p>
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-5xl space-y-8 px-4 py-8 sm:px-6">
        {jobId === null ? (
          <form onSubmit={onSubmit} className="grid grid-cols-1 gap-6 lg:grid-cols-5">
            <div className="lg:col-span-3">
              <Dropzone
                file={file}
                previewUrl={previewUrl}
                onSelect={(next) => {
                  setFile(next);
                  setFormError(null);
                }}
                onClear={() => setFile(null)}
                disabled={busy}
              />
            </div>

            <div className="space-y-5 rounded-xl border border-slate-200 bg-white p-5 lg:col-span-2 dark:border-slate-800 dark:bg-slate-900">
              <TransformForm value={form} onChange={setForm} disabled={busy} />

              {(formError || createJob.error) && (
                <div
                  role="alert"
                  className="rounded-lg bg-red-50 p-3 text-sm dark:bg-red-950/40"
                >
                  <p className="font-medium text-red-900 dark:text-red-200">
                    {formError ?? createJob.error?.message}
                  </p>
                  {createJob.error?.requestId && (
                    <p className="mt-1 font-mono text-xs text-red-700 dark:text-red-400">
                      Request ID: {createJob.error.requestId}
                    </p>
                  )}
                </div>
              )}

              <button
                type="submit"
                disabled={busy || !file}
                className="w-full rounded-md bg-indigo-600 px-4 py-2.5 font-medium text-white shadow-sm transition hover:bg-indigo-700 focus:ring-2 focus:ring-indigo-500/40 focus:outline-none disabled:cursor-not-allowed disabled:opacity-50"
              >
                {createJob.isPending ? "Uploading…" : "Transform"}
              </button>
            </div>
          </form>
        ) : (
          <JobResult
            job={job.data}
            error={job.error}
            isLoading={job.isPending}
            originalUrl={previewUrl}
            onReset={reset}
          />
        )}

        <Gallery entries={gallery.entries} onRemove={gallery.remove} onClear={gallery.clear} />
      </main>

      <footer className="mx-auto max-w-5xl px-4 pb-8 text-xs text-slate-400 sm:px-6">
        History is kept in this browser only; the API has no per-user job listing.
      </footer>
    </div>
  );
}
