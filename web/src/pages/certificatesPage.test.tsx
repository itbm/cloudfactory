import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { getState } from "../api/store";
import type { Role } from "../api/types";
import { reset, signedIn, website, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

async function open(role: Role, path: string): Promise<PanelApi> {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={[path]}><App /></MemoryRouter>);
  return api;
}

const table = () => screen.getByRole("heading", { name: "Certificates", level: 2 }).closest("section")!;

/** A certificate as the API gives it. */
const apiCertificate = (over: Record<string, unknown> = {}) => ({
  id: "cert-1", customerId: getState().certificates[0].customerId, source: "acme", names: ["acme.example.com", "*.acme.example.com"], state: "issuing",
  issuer: null, fingerprint: null, notBefore: null, expiresAt: null, trusted: null, renewAt: null,
  order: { stage: "new", startedAt: "2026-11-14T10:00:00+00:00", records: [] }, error: null, nextTryAt: null, usedBy: [],
  createdAt: "2026-11-14T10:00:00+00:00", updatedAt: "2026-11-14T10:00:00+00:00", ...over,
});

describe("the certificates page", () => {
  it("shows an owner the certificates of the customer, with the domains that use each one", async () => {
    await open("owner", "/certificates");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Certificates");
    const rows = within(table()).getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getByRole("link").textContent)).toEqual(["acme.example.com, *.acme.example.com", "acme-outlet.example.com, *.acme-outlet.example.com"]);
    expect(rows[0].textContent).toContain("Let's Encrypt, DNS validation");
    expect(rows[0].textContent).toContain("Valid");
    expect(rows[0].textContent).toContain("7 domains");
    expect(rows[1].textContent).toContain("Failed");
    expect(table().textContent).not.toContain("cornerbakery.example.org");
  });

  it("shows an administrator the certificates of all customers", async () => {
    await open("administrator", "/certificates");
    const text = table().textContent!;
    for (const name of ["cornerbakery.example.org", "acme.example.com", "acme-outlet.example.com"]) expect(text).toContain(name);
    expect(text).toContain("Corner Bakery");
    expect(text).toContain("Uploaded");
  });

  it("shows a certificate with the domains that use it and its chain", async () => {
    const api = await signedIn("owner");
    const c = getState().certificates.find((x) => x.names[0] === "acme.example.com")!;
    render(<MemoryRouter initialEntries={[`/certificates/${c.id}`]}><App /></MemoryRouter>);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("acme.example.com");
    const used = screen.getByRole("heading", { name: "Used by" }).closest("section")!;
    expect(within(used).getByRole("link", { name: "shop.acme.example.com" })).toBeTruthy();
    await waitFor(() => expect(screen.getByLabelText("Certificate chain in PEM format").textContent).toContain("-----BEGIN CERTIFICATE-----"));
    expect(screen.getByRole("main").textContent).not.toContain("PRIVATE KEY");
    // The panel renews it by itself. The user can also renew it now.
    api.on("POST /certificates/*/renew", () => [202, apiCertificate({ id: c.id, state: "renewing", order: { stage: "new", startedAt: "2026-11-14T10:00:00+00:00", records: [] } })]);
    fireEvent.click(screen.getByRole("button", { name: "Renew now" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Renewal" })).toBeTruthy());
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: `/certificates/${c.id}/renew` });
  });

  it("gets a wildcard certificate for a zone of the customer and shows the order", async () => {
    const api = await open("owner", "/certificates");
    api.on("POST /certificates", () => [201, apiCertificate()]);
    api.on("GET /certificates/cert-1", () => apiCertificate({
      order: { stage: "dns", startedAt: "2026-11-14T10:00:00+00:00", records: [{ name: "_acme-challenge.acme.example.com", value: "abc123" }, { name: "_acme-challenge.acme.example.com", value: "def456" }] },
    }));
    fireEvent.click(screen.getByRole("button", { name: "Get a wildcard certificate" }));
    // The first zone that the registrar sends to the panel.
    expect((screen.getByLabelText("Domain") as HTMLInputElement).value).toBe("acme.example.com");
    expect(screen.getByRole("dialog").textContent).toContain("*.acme.example.com");
    fireEvent.click(screen.getByRole("button", { name: "Get certificate" }));
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("acme.example.com"));
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: "/certificates", body: { source: "acme", domain: "acme.example.com", wildcard: true } });
    await waitFor(() => expect(screen.getByText("def456")).toBeTruthy());
    const stages = screen.getByRole("list", { name: "Stages of the order" });
    expect(within(stages).getAllByRole("listitem").map((li) => li.getAttribute("class"))).toEqual(["cluster-step cluster-step-done", "cluster-step cluster-step-now", "cluster-step cluster-step-todo", "cluster-step cluster-step-todo"]);
    expect(stages.querySelector("[aria-current=step]")?.textContent).toContain("Wait for the DNS servers");
  });

  it("shows the error of the API next to the domain", async () => {
    const api = await open("owner", "/certificates");
    api.on("POST /certificates", () => [422, { message: "x", errors: { domain: ["The panel cannot add DNS records for shop.example. Make a DNS zone for the domain on the DNS page."] } }]);
    fireEvent.click(screen.getByRole("button", { name: "Get a wildcard certificate" }));
    fireEvent.change(screen.getByLabelText("Domain"), { target: { value: "shop.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Get certificate" }));
    await waitFor(() => expect(screen.getByRole("dialog").textContent).toContain("The panel cannot add DNS records for shop.example."));
  });

  it("uploads a certificate and its key", async () => {
    const api = await open("owner", "/certificates");
    const uploaded = () => apiCertificate({ id: "cert-2", source: "upload", names: ["shop.acme.example.com"], state: "valid", order: null, issuer: "Example CA" });
    api.on("POST /certificates", () => [201, uploaded()]);
    // The list of the panel has the new certificate now.
    api.change<{ data: unknown[] }>("/certificates", (b) => { b.data.push(uploaded()); });
    api.on("GET /certificates/cert-2", () => apiCertificate({ id: "cert-2", source: "upload", names: ["shop.acme.example.com"], state: "valid", order: null, issuer: "Example CA", certificatePem: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n" }));
    fireEvent.click(screen.getByRole("button", { name: "Upload certificate" }));
    fireEvent.change(screen.getByLabelText("Certificate chain"), { target: { value: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n" } });
    fireEvent.change(screen.getByLabelText("Private key"), { target: { value: "-----BEGIN PRIVATE KEY-----\nMIGH\n-----END PRIVATE KEY-----\n" } });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Upload certificate" }));
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("shop.acme.example.com"));
    expect(api.changes.at(-1)).toMatchObject({ method: "POST", path: "/certificates", body: { source: "upload", certificate: expect.stringContaining("BEGIN CERTIFICATE"), privateKey: expect.stringContaining("BEGIN PRIVATE KEY") } });
  });

  it("shows on the domains of a website which certificate each domain uses", async () => {
    await signedIn("owner");
    const shop = website("shop.acme.example.com");
    render(<MemoryRouter initialEntries={[`/websites/${shop.id}/domains`]}><App /></MemoryRouter>);
    const row = (await screen.findByText("shop.acme.example.com", { selector: "strong" })).closest("tr")!;
    expect(within(row).getByRole("link", { name: "Certificate with DNS validation" }).getAttribute("href")).toBe(`/certificates/${shop.domains[0].certificate.certificateId}`);
  });
});
