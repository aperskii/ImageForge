import { useCallback, useRef, useState } from "react";

interface CompareSliderProps {
  beforeUrl: string;
  afterUrl: string;
  beforeLabel?: string;
  afterLabel?: string;
}

/**
 * A before/after comparison with a draggable divider.
 *
 * The two images are stacked and the top one is clipped to the handle's
 * position, so both stay pixel-aligned however the container is sized. The
 * handle is a real range input underneath a custom-drawn one, which is what
 * makes it keyboard-operable and announced to a screen reader for free.
 */
export function CompareSlider({
  beforeUrl,
  afterUrl,
  beforeLabel = "Original",
  afterLabel = "Result",
}: CompareSliderProps) {
  const [position, setPosition] = useState(50);
  const containerRef = useRef<HTMLDivElement>(null);

  const moveTo = useCallback((clientX: number) => {
    const container = containerRef.current;
    if (!container) return;

    const bounds = container.getBoundingClientRect();
    if (bounds.width === 0) return;

    const ratio = ((clientX - bounds.left) / bounds.width) * 100;
    setPosition(Math.min(100, Math.max(0, ratio)));
  }, []);

  const onPointerDown = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      // Only drag with the primary button; a right-click should open the menu.
      if (event.button !== 0) return;
      event.currentTarget.setPointerCapture(event.pointerId);
      moveTo(event.clientX);
    },
    [moveTo],
  );

  const onPointerMove = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      if (!event.currentTarget.hasPointerCapture(event.pointerId)) return;
      moveTo(event.clientX);
    },
    [moveTo],
  );

  return (
    <div className="space-y-3">
      <div
        ref={containerRef}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        className="relative touch-none select-none overflow-hidden rounded-xl bg-slate-100 ring-1 ring-slate-200 dark:bg-slate-800 dark:ring-slate-700"
      >
        {/* The result sets the box size; the original is overlaid on top of it. */}
        <img
          src={afterUrl}
          alt={afterLabel}
          className="block max-h-[60vh] w-full object-contain"
          draggable={false}
        />

        <div
          className="absolute inset-0 overflow-hidden"
          style={{ clipPath: `inset(0 ${100 - position}% 0 0)` }}
        >
          <img
            src={beforeUrl}
            alt={beforeLabel}
            className="block h-full w-full object-contain"
            draggable={false}
          />
        </div>

        {/* The divider. It is decorative: the range input below is what is
            actually operated, including by keyboard. */}
        <div
          className="pointer-events-none absolute inset-y-0 w-0.5 bg-white shadow-[0_0_0_1px_rgba(0,0,0,0.25)]"
          style={{ left: `${position}%` }}
          aria-hidden="true"
        >
          <div className="absolute top-1/2 left-1/2 flex h-9 w-9 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full bg-white text-slate-700 shadow-md">
            <svg className="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M8 7l-5 5 5 5M16 7l5 5-5 5" />
            </svg>
          </div>
        </div>

        <span className="pointer-events-none absolute top-3 left-3 rounded bg-black/60 px-2 py-0.5 text-xs font-medium text-white">
          {beforeLabel}
        </span>
        <span className="pointer-events-none absolute top-3 right-3 rounded bg-black/60 px-2 py-0.5 text-xs font-medium text-white">
          {afterLabel}
        </span>
      </div>

      <label className="block">
        <span className="sr-only">
          Comparison position: {Math.round(position)}% {beforeLabel}
        </span>
        <input
          type="range"
          min={0}
          max={100}
          value={position}
          onChange={(event) => setPosition(Number(event.target.value))}
          className="w-full accent-indigo-600"
          aria-label={`Reveal more of ${beforeLabel} or ${afterLabel}`}
        />
      </label>
    </div>
  );
}
