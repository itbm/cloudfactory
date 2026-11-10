import type { AuditEntry } from "../api/types";

/**
 * Audit finding S11: a spreadsheet reads a cell that starts with =, +, - or @ as a formula.
 * A tab or a carriage return at the start has the same effect. The export puts a single
 * quotation mark before such a cell, so that the spreadsheet shows it as text.
 */
export function csvCell(value: string): string {
  const safe = /^[=+\-@\t\r]/.test(value) ? `'${value}` : value;
  return `"${safe.replace(/"/g, '""')}"`;
}

/** The audit log as a CSV file (FR-105). */
export function auditCsv(rows: AuditEntry[]): string {
  return ["time,person,ip,change,target", ...rows.map((a) => [a.at, a.actor, a.ip, a.action, a.target].map(csvCell).join(","))].join("\n");
}
