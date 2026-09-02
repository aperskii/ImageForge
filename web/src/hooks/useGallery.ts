import { useCallback, useEffect, useState } from "react";
import type { Job } from "../api/types";

const STORAGE_KEY = "imageforge.gallery.v1";

/** How many past jobs to keep. Thumbnails are data URLs, so this is bounded by
 * what fits in localStorage, not by taste. */
const MAX_ENTRIES = 24;

/** Longest edge of a stored thumbnail, in pixels. */
const THUMBNAIL_SIZE = 160;

/** A job the browser remembers, with enough of the source to show a thumbnail. */
export interface GalleryEntry {
  id: string;
  status: Job["status"];
  fileName: string;
  createdAt: string;
  /** A small JPEG data URL of the source image. */
  thumbnail: string;
  width: number;
  height: number;
  format: string;
  resultUrl?: string;
  error?: string;
}

/** Reads the gallery, tolerating anything that is not what we wrote. */
function load(): GalleryEntry[] {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? (parsed as GalleryEntry[]) : [];
  } catch {
    // A private window, cleared site data, or a older shape we no longer
    // understand. An empty gallery is the right answer to all of them.
    return [];
  }
}

/** Writes the gallery, giving up quietly when the quota is gone. */
function save(entries: GalleryEntry[]): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(entries));
  } catch {
    // Thumbnails are the bulk of this, and losing history is not worth
    // interrupting an upload over.
  }
}

/**
 * The gallery of past jobs, kept in the browser.
 *
 * There is no per-user job listing on the server, so history is local to this
 * browser and disappears with its site data.
 */
export function useGallery() {
  const [entries, setEntries] = useState<GalleryEntry[]>(load);

  useEffect(() => {
    save(entries);
  }, [entries]);

  const add = useCallback((entry: GalleryEntry) => {
    setEntries((current) => [entry, ...current.filter((e) => e.id !== entry.id)].slice(0, MAX_ENTRIES));
  }, []);

  /** Updates an entry in place as its job progresses. */
  const update = useCallback((id: string, patch: Partial<GalleryEntry>) => {
    setEntries((current) => {
      const index = current.findIndex((entry) => entry.id === id);
      if (index === -1) return current;

      const existing = current[index];
      if (!existing) return current;

      const next = [...current];
      next[index] = { ...existing, ...patch };
      return next;
    });
  }, []);

  const remove = useCallback((id: string) => {
    setEntries((current) => current.filter((entry) => entry.id !== id));
  }, []);

  const clear = useCallback(() => setEntries([]), []);

  return { entries, add, update, remove, clear };
}

/**
 * Renders a small JPEG data URL from an image file.
 *
 * The thumbnail is generated in the browser rather than fetched back from the
 * server, so the gallery works even when results are not publicly readable.
 */
export async function makeThumbnail(file: File): Promise<string> {
  const bitmap = await createImageBitmap(file);
  try {
    const scale = Math.min(1, THUMBNAIL_SIZE / Math.max(bitmap.width, bitmap.height));
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));

    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;

    const context = canvas.getContext("2d");
    if (!context) {
      throw new Error("this browser did not provide a 2d canvas context");
    }
    context.drawImage(bitmap, 0, 0, width, height);

    return canvas.toDataURL("image/jpeg", 0.7);
  } finally {
    bitmap.close();
  }
}
