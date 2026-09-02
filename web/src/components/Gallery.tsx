import type { GalleryEntry } from "../hooks/useGallery";
import { StatusBadge } from "./JobResult";

interface GalleryProps {
  entries: GalleryEntry[];
  onRemove: (id: string) => void;
  onClear: () => void;
}

/** Renders a timestamp as something readable at a glance. */
function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";

  const seconds = Math.round((Date.now() - then) / 1000);
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function Gallery({ entries, onRemove, onClear }: GalleryProps) {
  if (entries.length === 0) {
    return (
      <section className="rounded-xl border border-dashed border-slate-300 p-8 text-center dark:border-slate-700">
        <p className="text-sm text-slate-500 dark:text-slate-400">
          Jobs you submit will appear here.
        </p>
      </section>
    );
  }

  return (
    <section>
      <div className="mb-3 flex items-center justify-between">
        <h2 className="font-semibold text-slate-900 dark:text-slate-100">
          Recent jobs
          <span className="ml-2 text-sm font-normal text-slate-500 dark:text-slate-400">
            {entries.length}
          </span>
        </h2>
        <button
          type="button"
          onClick={onClear}
          className="text-sm text-slate-500 underline-offset-2 transition hover:text-slate-900 hover:underline dark:text-slate-400 dark:hover:text-slate-100"
        >
          Clear
        </button>
      </div>

      <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        {entries.map((entry) => (
          <li
            key={entry.id}
            className="group relative overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900"
          >
            <div className="aspect-square overflow-hidden bg-slate-100 dark:bg-slate-800">
              <img
                src={entry.thumbnail}
                alt={entry.fileName}
                loading="lazy"
                className="h-full w-full object-cover"
              />
            </div>

            <div className="space-y-1.5 p-3">
              <p
                className="truncate text-sm font-medium text-slate-900 dark:text-slate-100"
                title={entry.fileName}
              >
                {entry.fileName}
              </p>
              <p className="text-xs text-slate-500 dark:text-slate-400">
                {entry.format.toUpperCase()} · {entry.width || "auto"}×{entry.height || "auto"} ·{" "}
                {relativeTime(entry.createdAt)}
              </p>
              <div className="flex items-center justify-between gap-2">
                <StatusBadge status={entry.status} />
                {entry.resultUrl && (
                  <a
                    href={entry.resultUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-xs font-medium text-indigo-600 underline-offset-2 hover:underline dark:text-indigo-400"
                  >
                    Open
                  </a>
                )}
              </div>
              {entry.error && (
                <p className="truncate text-xs text-red-600 dark:text-red-400" title={entry.error}>
                  {entry.error}
                </p>
              )}
            </div>

            <button
              type="button"
              onClick={() => onRemove(entry.id)}
              aria-label={`Remove ${entry.fileName} from the gallery`}
              className="absolute top-2 right-2 rounded-full bg-black/60 p-1 text-white opacity-0 transition group-hover:opacity-100 focus:opacity-100"
            >
              <svg className="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.5}>
                <path strokeLinecap="round" d="M6 6l12 12M18 6L6 18" />
              </svg>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
