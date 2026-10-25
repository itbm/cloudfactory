import { describe, expect, it } from "vitest";
import type { DnsRecord } from "../api/types";
import { isIpv6, validateRecord } from "./dns";

const base = { name: "www", ttl: 3600 };

describe("validateRecord", () => {
  it("accepts correct records", () => {
    expect(validateRecord({ ...base, type: "A", value: "203.0.113.10" })).toEqual({});
    expect(validateRecord({ ...base, type: "AAAA", value: "2001:db8::10" })).toEqual({});
    expect(validateRecord({ ...base, type: "CNAME", value: "example.com." })).toEqual({});
    expect(validateRecord({ ...base, type: "MX", value: "mail.example.com", priority: 10 })).toEqual({});
    expect(validateRecord({ ...base, type: "CAA", value: '0 issue "letsencrypt.org"' })).toEqual({});
    expect(validateRecord({ ...base, name: "_dmarc", type: "TXT", value: "v=DMARC1; p=none" })).toEqual({});
  });

  it("rejects incorrect values", () => {
    expect(validateRecord({ ...base, type: "A", value: "203.0.113.300" }).value).toBeDefined();
    expect(validateRecord({ ...base, type: "AAAA", value: "2001:db8:::1" }).value).toBeDefined();
    expect(validateRecord({ ...base, type: "MX", value: "mail.example.com" }).priority).toBeDefined();
    expect(validateRecord({ ...base, type: "A", value: "203.0.113.1", ttl: 10 }).ttl).toBeDefined();
    expect(validateRecord({ ...base, name: "bad name", type: "A", value: "203.0.113.1" }).name).toBeDefined();
  });

  it("stops a CNAME next to other records", () => {
    const existing: DnsRecord[] = [{ id: "r1", type: "A", name: "www", value: "203.0.113.10", ttl: 3600 }];
    expect(validateRecord({ ...base, type: "CNAME", value: "example.com." }, existing).name).toMatch(/CNAME/);
    expect(validateRecord({ ...base, type: "CNAME", value: "example.com." }, existing, "r1")).toEqual({});
  });

  it("stops duplicates", () => {
    const existing: DnsRecord[] = [{ id: "r1", type: "A", name: "www", value: "203.0.113.10", ttl: 3600 }];
    expect(validateRecord({ ...base, type: "A", value: "203.0.113.10" }, existing).value).toBe("This record exists.");
  });

  it("checks IPv6", () => {
    expect(isIpv6("::1")).toBe(true);
    expect(isIpv6("2001:0db8:0000:0000:0000:ff00:0042:8329")).toBe(true);
    expect(isIpv6("2001:db8")).toBe(false);
  });
});
