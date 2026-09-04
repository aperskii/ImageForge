import { FORMATS, LIBVIPS_ONLY_FORMATS, MAX_DIMENSION, isLossless } from "../api/types";
import type { Format, TransformationSpec } from "../api/types";

/** The form's own state, where the dimensions are strings because an empty
 * input is a meaningful value: "derive this one from the other". */
export interface FormState {
  width: string;
  height: string;
  format: Format;
  quality: number;
  watermark: boolean;
  stripMetadata: boolean;
}

export const INITIAL_FORM: FormState = {
  width: "800",
  height: "",
  format: "jpeg",
  quality: 82,
  watermark: false,
  stripMetadata: true,
};

/** Turns the form into the API's spec shape. */
export function toSpec(form: FormState): TransformationSpec {
  return {
    width: Number(form.width) || 0,
    height: Number(form.height) || 0,
    format: form.format,
    // The API rejects a quality on a lossless format, so send none.
    quality: isLossless(form.format) ? 0 : form.quality,
    watermark: form.watermark,
    strip_metadata: form.stripMetadata,
  };
}

/**
 * Reports why the form cannot be submitted, or null when it can.
 *
 * These mirror the server's own rules so the common mistakes are caught before
 * a round trip. The server remains the authority: it validates again.
 */
export function validate(form: FormState): string | null {
  const width = Number(form.width) || 0;
  const height = Number(form.height) || 0;

  if (form.width !== "" && !Number.isInteger(Number(form.width))) {
    return "Width must be a whole number of pixels.";
  }
  if (form.height !== "" && !Number.isInteger(Number(form.height))) {
    return "Height must be a whole number of pixels.";
  }
  if (width <= 0 && height <= 0) {
    return "Set a width or a height. Leave one empty to keep the original aspect ratio.";
  }
  if (width < 0 || height < 0) {
    return "Dimensions cannot be negative.";
  }
  if (width > MAX_DIMENSION || height > MAX_DIMENSION) {
    return `Dimensions cannot exceed ${MAX_DIMENSION}px.`;
  }
  return null;
}

interface TransformFormProps {
  value: FormState;
  onChange: (next: FormState) => void;
  disabled?: boolean;
}

const inputClass =
  "w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-slate-900 shadow-sm transition focus:border-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500/30 disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100";

const labelClass = "block text-sm font-medium text-slate-700 dark:text-slate-300";

export function TransformForm({ value, onChange, disabled = false }: TransformFormProps) {
  const set = <K extends keyof FormState>(key: K, next: FormState[K]) =>
    onChange({ ...value, [key]: next });

  const lossless = isLossless(value.format);

  return (
    <fieldset disabled={disabled} className="space-y-5">
      <legend className="sr-only">Transformation options</legend>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <label htmlFor="width" className={labelClass}>
            Width <span className="font-normal text-slate-400">(px)</span>
          </label>
          <input
            id="width"
            type="number"
            inputMode="numeric"
            min={0}
            max={MAX_DIMENSION}
            placeholder="auto"
            value={value.width}
            onChange={(event) => set("width", event.target.value)}
            className={`mt-1 ${inputClass}`}
          />
        </div>

        <div>
          <label htmlFor="height" className={labelClass}>
            Height <span className="font-normal text-slate-400">(px)</span>
          </label>
          <input
            id="height"
            type="number"
            inputMode="numeric"
            min={0}
            max={MAX_DIMENSION}
            placeholder="auto"
            value={value.height}
            onChange={(event) => set("height", event.target.value)}
            className={`mt-1 ${inputClass}`}
          />
        </div>
      </div>

      <p className="-mt-3 text-xs text-slate-500 dark:text-slate-400">
        Leave one empty to derive it from the other and keep the aspect ratio. Set both to resize to
        exactly that size.
      </p>

      <div>
        <label htmlFor="format" className={labelClass}>
          Output format
        </label>
        <select
          id="format"
          value={value.format}
          onChange={(event) => set("format", event.target.value as Format)}
          className={`mt-1 ${inputClass}`}
        >
          {FORMATS.map((format) => (
            <option key={format} value={format}>
              {format.toUpperCase()}
              {LIBVIPS_ONLY_FORMATS.includes(format) ? " — needs the libvips backend" : ""}
            </option>
          ))}
        </select>
        {LIBVIPS_ONLY_FORMATS.includes(value.format) && (
          <p className="mt-1 text-xs text-amber-600 dark:text-amber-500">
            A server built with <code className="font-mono">-tags nogovips</code> cannot encode this
            format and will reject the job.
          </p>
        )}
      </div>

      <div>
        <div className="flex items-baseline justify-between">
          <label htmlFor="quality" className={labelClass}>
            Quality
          </label>
          <span className="text-sm tabular-nums text-slate-500 dark:text-slate-400">
            {lossless ? "n/a" : value.quality}
          </span>
        </div>
        <input
          id="quality"
          type="range"
          min={1}
          max={100}
          value={value.quality}
          disabled={lossless}
          onChange={(event) => set("quality", Number(event.target.value))}
          className="mt-2 w-full accent-indigo-600 disabled:opacity-40"
        />
        {lossless && (
          <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
            PNG is lossless, so quality does not apply.
          </p>
        )}
      </div>

      <div className="space-y-3">
        <label className="flex items-center gap-3">
          <input
            type="checkbox"
            checked={value.watermark}
            onChange={(event) => set("watermark", event.target.checked)}
            className="h-4 w-4 rounded accent-indigo-600"
          />
          <span className="text-sm text-slate-700 dark:text-slate-300">
            Add a watermark
            <span className="block text-xs text-slate-500 dark:text-slate-400">
              Overlaid in the bottom-right corner.
            </span>
          </span>
        </label>

        <label className="flex items-center gap-3">
          <input
            type="checkbox"
            checked={value.stripMetadata}
            onChange={(event) => set("stripMetadata", event.target.checked)}
            className="h-4 w-4 rounded accent-indigo-600"
          />
          <span className="text-sm text-slate-700 dark:text-slate-300">
            Strip metadata
            <span className="block text-xs text-slate-500 dark:text-slate-400">
              Removes the camera's EXIF, including any GPS location. The
              encoder still writes orientation and resolution.
            </span>
          </span>
        </label>
      </div>
    </fieldset>
  );
}
