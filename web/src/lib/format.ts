// Formatting helpers. All output uses British English (NFR-09).

const LOCALE = "en-GB";
const rtf = new Intl.RelativeTimeFormat(LOCALE, { numeric: "auto" });

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
];

/** "3 days ago", "yesterday", "in 62 days". */
export function relativeTime(iso: string, now = Date.now()): string {
  const seconds = Math.round((new Date(iso).getTime() - now) / 1000);
  const abs = Math.abs(seconds);
  for (const [unit, size] of UNITS) {
    if (abs >= size) {
      // Show weeks only below one month, and days below two weeks.
      if (unit === "week" && abs < 14 * 24 * 3600) continue;
      return rtf.format(Math.round(seconds / size), unit);
    }
  }
  return "just now";
}

/** Whole days from now to the date. */
export function daysUntil(iso: string, now = Date.now()): number {
  return Math.round((new Date(iso).getTime() - now) / (24 * 3600 * 1000));
}

/** "12 min ago" style, short, for activity lists. Times from 6 to 24 hours ago show the clock time. */
export function shortAgo(iso: string, now = Date.now()): string {
  const minutes = Math.round((now - new Date(iso).getTime()) / 60_000);
  if (minutes < 1) return "now";
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 6) return `${hours} h ago`;
  if (hours < 24) return clockTime(iso);
  return relativeTime(iso, now);
}

export function clockTime(iso: string): string {
  return new Date(iso).toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" });
}

export function longDate(iso: string): string {
  return new Date(iso).toLocaleDateString(LOCALE, { day: "numeric", month: "long", year: "numeric" });
}

export function shortDate(iso: string): string {
  return new Date(iso).toLocaleDateString(LOCALE, { day: "numeric", month: "short", year: "numeric" });
}

/** Binary units with the labels that the mockups use: "742 MB", "9.4 GB". */
export function bytes(value: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = value;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const digits = v >= 100 || Number.isInteger(v) ? 0 : 1;
  return `${v.toFixed(digits)} ${units[i]}`;
}

export function percent(value: number): string {
  if (value > 0 && value < 1) return "<1%";
  return `${Math.round(value)}%`;
}

export function ratio(used: number, limit: number): number {
  if (limit <= 0) return 0;
  return Math.min(1, Math.max(0, used / limit));
}
