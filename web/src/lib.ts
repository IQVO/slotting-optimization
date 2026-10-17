import { NO_VALUE } from "@warehouse/ui-kit";

/** "2026-10-09 08:05Z" for an RFC 3339 instant; the raw text when unparseable. */
export function utcLabel(iso: string | undefined): string {
  if (!iso) return NO_VALUE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t).toISOString();
  return `${d.slice(0, 10)} ${d.slice(11, 16)}Z`;
}

/** "2026-09-10 → 2026-10-08" for a plan's demand window (inclusive → exclusive). */
export function windowLabel(from: string, to: string): string {
  const f = Date.parse(from);
  const t = Date.parse(to);
  if (Number.isNaN(f) || Number.isNaN(t)) return `${from} → ${to}`;
  return `${new Date(f).toISOString().slice(0, 10)} → ${new Date(t).toISOString().slice(0, 10)}`;
}

/**
 * A strictly whole, positive number typed into a field, or null when the
 * text is empty or not a whole positive number ("12.5", "1e3", "abc", "0").
 */
export function positiveWhole(raw: string): number | null {
  const s = raw.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

/** Site-id rule of the API: 1..64 characters, no whitespace, control
 *  characters or "/" (invalid-site-id otherwise). Empty means "the default
 *  site", which is fine -- so "" is valid. */
export function siteIdProblem(siteId: string): string | null {
  if (siteId.length === 0) return null; // the service's configured default site
  if (siteId.length > 64) return "A site id is at most 64 characters.";
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(siteId)) return "A site id cannot contain spaces, control characters or '/'.";
  return null;
}

/** Reject-reason rule of the API: optional, at most 500 characters
 *  (reject-reason-too-long otherwise). */
export function reasonProblem(reason: string): string | null {
  if (reason.length > 500) return "A reject reason is at most 500 characters.";
  return null;
}

/** "→ WH1-FWD-A01-01-02-A" / "WH1-FWD-A02-03-01-A →" / "" for a move's slots. */
export function movePath(fromSlot: string | undefined, toSlot: string | undefined): string {
  const from = fromSlot || "";
  const to = toSlot || "";
  if (from && to) return `${from} → ${to}`;
  if (to) return `→ ${to}`;
  if (from) return `${from} →`;
  return "";
}
