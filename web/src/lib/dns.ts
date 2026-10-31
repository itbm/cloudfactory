// Checks for DNS records before the panel saves them (FR-702).

import type { DnsRecord, DnsRecordType } from "../api/types";

export const RECORD_TYPES: DnsRecordType[] = ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"];

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const LABEL = /^(?!-)[a-z0-9-]{1,63}(?<!-)$/i;

export function isHostname(value: string): boolean {
  const v = value.replace(/\.$/, "");
  if (!v || v.length > 253) return false;
  return v.split(".").every((l) => LABEL.test(l) || l === "_domainkey" || /^_[a-z0-9-]+$/i.test(l));
}

export function isIpv6(value: string): boolean {
  if (!/^[0-9a-f:]+$/i.test(value) || !value.includes(":") || value.includes(":::")) return false;
  const doubles = value.split("::").length - 1;
  if (doubles > 1) return false;
  const groups = value.split(":").filter((g) => g !== "");
  if (groups.some((g) => g.length > 4)) return false;
  return doubles === 1 ? groups.length < 8 : groups.length === 8;
}

export interface RecordInput {
  type: DnsRecordType;
  name: string;
  value: string;
  ttl: number;
  priority?: number;
}

export type RecordErrors = Partial<Record<"name" | "value" | "ttl" | "priority", string>>;

/** Returns the errors for a record. An empty object means the record is correct. */
export function validateRecord(input: RecordInput, existing: DnsRecord[] = [], editingId?: string): RecordErrors {
  const errors: RecordErrors = {};
  const name = input.name.trim() || "@";
  const value = input.value.trim();

  if (name !== "@" && name !== "*" && !isHostname(name.replace(/^\*\./, ""))) {
    errors.name = "Use a name such as @, www or mail. Use letters, digits and hyphens only.";
  }
  if (!Number.isInteger(input.ttl) || input.ttl < 60 || input.ttl > 604800) {
    errors.ttl = "Use a TTL from 60 to 604800 seconds.";
  }
  if (!value) {
    errors.value = "Type a value.";
    return errors;
  }

  switch (input.type) {
    case "A":
      if (!IPV4.test(value)) errors.value = "Type an IPv4 address, for example 203.0.113.10.";
      break;
    case "AAAA":
      if (!isIpv6(value)) errors.value = "Type an IPv6 address, for example 2001:db8::10.";
      break;
    case "CNAME":
    case "NS":
      if (!isHostname(value)) errors.value = "Type a hostname, for example example.com.";
      break;
    case "MX":
      if (!isHostname(value)) errors.value = "Type the hostname of the mail server.";
      if (input.priority === undefined || input.priority < 0 || input.priority > 65535) {
        errors.priority = "Use a priority from 0 to 65535.";
      }
      break;
    case "SRV":
      if (!/^\d+ \d+ \S+$/.test(value)) errors.value = "Use the format: weight port target, for example 5 5060 sip.example.com.";
      if (input.priority === undefined || input.priority < 0 || input.priority > 65535) {
        errors.priority = "Use a priority from 0 to 65535.";
      }
      break;
    case "CAA":
      if (!/^\d{1,3} (issue|issuewild|iodef) "[^"]*"$/.test(value)) errors.value = 'Use the format: 0 issue "letsencrypt.org".';
      break;
    case "TXT":
      if (value.length > 4096) errors.value = "A TXT value must be 4096 characters or fewer.";
      break;
  }

  const others = existing.filter((r) => r.id !== editingId && r.name === name);
  if (input.type === "CNAME" && others.length > 0) {
    errors.name = "A CNAME record cannot share its name with other records.";
  } else if (others.some((r) => r.type === "CNAME")) {
    errors.name = "This name has a CNAME record. Other records cannot use the same name.";
  }
  if (others.some((r) => r.type === input.type && r.value === value)) {
    errors.value = "This record exists.";
  }
  return errors;
}
