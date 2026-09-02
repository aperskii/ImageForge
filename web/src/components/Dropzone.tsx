import { useDropzone, type FileRejection } from "react-dropzone";
import { useCallback } from "react";

/** Formats the browser can hand us as a source image. */
const ACCEPTED = {
  "image/jpeg": [".jpg", ".jpeg"],
  "image/png": [".png"],
  "image/webp": [".webp"],
  "image/gif": [".gif"],
  "image/avif": [".avif"],
} as const;

/** Must match the API's own limit, so a too-large file is caught before it is
 * sent rather than after ten megabytes of upload. */
const MAX_BYTES = 10 * 1024 * 1024;

interface DropzoneProps {
  file: File | null;
  previewUrl: string | null;
  onSelect: (file: File) => void;
  onClear: () => void;
  disabled?: boolean;
}

/** Renders a byte count the way a person would say it. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function Dropzone({ file, previewUrl, onSelect, onClear, disabled = false }: DropzoneProps) {
  const onDrop = useCallback(
    (accepted: File[]) => {
      const first = accepted[0];
      if (first) onSelect(first);
    },
    [onSelect],
  );

  const { getRootProps, getInputProps, isDragActive, isDragReject, fileRejections } = useDropzone({
    onDrop,
    accept: ACCEPTED,
    maxSize: MAX_BYTES,
    multiple: false,
    disabled,
  });

  const rejection: FileRejection | undefined = fileRejections[0];

  if (file && previewUrl) {
    return (
      <div className="rounded-xl border border-slate-300 bg-white p-4 dark:border-slate-700 dark:bg-slate-900">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
          <img
            src={previewUrl}
            alt={`Preview of ${file.name}`}
            className="h-32 w-32 shrink-0 rounded-lg object-cover ring-1 ring-slate-200 dark:ring-slate-700"
          />
          <div className="min-w-0 flex-1">
            <p className="truncate font-medium text-slate-900 dark:text-slate-100">{file.name}</p>
            <p className="text-sm text-slate-500 dark:text-slate-400">
              {formatBytes(file.size)} · {file.type || "unknown type"}
            </p>
            <button
              type="button"
              onClick={onClear}
              disabled={disabled}
              className="mt-3 rounded-md px-3 py-1.5 text-sm font-medium text-slate-700 ring-1 ring-slate-300 transition hover:bg-slate-100 disabled:opacity-50 dark:text-slate-200 dark:ring-slate-600 dark:hover:bg-slate-800"
            >
              Choose a different image
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div>
      <div
        {...getRootProps()}
        className={[
          "flex cursor-pointer flex-col items-center justify-center rounded-xl border-2 border-dashed px-6 py-12 text-center transition",
          disabled ? "cursor-not-allowed opacity-60" : "",
          isDragReject
            ? "border-red-400 bg-red-50 dark:border-red-500/60 dark:bg-red-950/30"
            : isDragActive
              ? "border-indigo-500 bg-indigo-50 dark:border-indigo-400 dark:bg-indigo-950/30"
              : "border-slate-300 bg-white hover:border-slate-400 dark:border-slate-700 dark:bg-slate-900 dark:hover:border-slate-600",
        ].join(" ")}
      >
        <input {...getInputProps()} />
        <svg
          className="mb-3 h-10 w-10 text-slate-400"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth={1.5}
          aria-hidden="true"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            d="M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5M16.5 12L12 7.5 7.5 12M12 7.5V21"
          />
        </svg>
        <p className="font-medium text-slate-900 dark:text-slate-100">
          {isDragActive ? "Drop the image here" : "Drag an image here, or click to choose one"}
        </p>
        <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
          JPEG, PNG, WebP, GIF or AVIF, up to {formatBytes(MAX_BYTES)}
        </p>
      </div>

      {rejection && (
        <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">
          {rejection.file.name}:{" "}
          {rejection.errors
            .map((error) =>
              error.code === "file-too-large"
                ? `it is ${formatBytes(rejection.file.size)}, over the ${formatBytes(MAX_BYTES)} limit`
                : error.code === "file-invalid-type"
                  ? "that file type cannot be used as a source image"
                  : error.message,
            )
            .join("; ")}
        </p>
      )}
    </div>
  );
}
