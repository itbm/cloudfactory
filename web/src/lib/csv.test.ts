import { describe, expect, it } from "vitest";
import { auditCsv, csvCell } from "./csv";

describe("the CSV export of the audit log (audit finding S11)", () => {
  it("does not let a spreadsheet read a cell as a formula", () => {
    expect(csvCell("=HYPERLINK(\"https://evil.example\",\"x\")")).toBe(`"'=HYPERLINK(""https://evil.example"",""x"")"`);
    for (const start of ["=", "+", "-", "@", "\t", "\r"]) expect(csvCell(`${start}1+1`)).toBe(`"'${start}1+1"`);
  });

  it("does not change other cells", () => {
    expect(csvCell("acme.example")).toBe(`"acme.example"`);
    expect(csvCell("a \"b\", c")).toBe(`"a ""b"", c"`);
    // The sign is not at the start of the cell.
    expect(csvCell("shop.example.net -cmd")).toBe(`"shop.example.net -cmd"`);
  });

  it("makes one line for each entry", () => {
    const csv = auditCsv([{ id: "1", at: "2026-11-10T10:00:00Z", actor: "Alex", ip: "203.0.113.5", action: "Created website", target: "=cmd|'/c calc'!A1" }]);
    expect(csv.split("\n")).toEqual(["time,person,ip,change,target", `"2026-11-10T10:00:00Z","Alex","203.0.113.5","Created website","'=cmd|'/c calc'!A1"`]);
  });
});
