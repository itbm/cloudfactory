import { describe, expect, it } from "vitest";
import { bytes, daysUntil, percent, relativeTime, shortAgo } from "./format";

const now = new Date("2026-10-25T12:00:00Z").getTime();
const at = (ms: number) => new Date(now + ms).toISOString();
const HOUR = 3600_000;
const DAY = 24 * HOUR;

describe("format", () => {
  it("formats relative times", () => {
    expect(relativeTime(at(-3 * DAY), now)).toBe("3 days ago");
    expect(relativeTime(at(-1 * DAY), now)).toBe("yesterday");
    expect(relativeTime(at(-2 * HOUR), now)).toBe("2 hours ago");
    expect(relativeTime(at(-25 * 60_000), now)).toBe("25 minutes ago");
    expect(relativeTime(at(-14 * DAY), now)).toBe("2 weeks ago");
    expect(relativeTime(at(-240 * DAY), now)).toBe("8 months ago");
  });

  it("formats short times", () => {
    expect(shortAgo(at(-12 * 60_000), now)).toBe("12 min ago");
    expect(shortAgo(at(-2 * HOUR), now)).toBe("2 h ago");
  });

  it("counts days", () => {
    expect(daysUntil(at(62 * DAY), now)).toBe(62);
  });

  it("formats bytes", () => {
    expect(bytes(742 * 1024 ** 2)).toBe("742 MB");
    expect(bytes(9.4 * 1024 ** 3)).toBe("9.4 GB");
    expect(bytes(2 * 1024 ** 3)).toBe("2 GB");
  });

  it("formats percentages", () => {
    expect(percent(0.4)).toBe("<1%");
    expect(percent(14)).toBe("14%");
  });
});
