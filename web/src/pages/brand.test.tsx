// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { getState, signIn } from "../api/store";
import { contrastWithWhite } from "./BrandPage";
import { panelApi, reset, signedIn } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
  document.documentElement.removeAttribute("style");
});

const nav = () => within(screen.getByRole("navigation", { name: "Main" }));
const NORTHWIND = { name: "Northwind Hosting", colour: "#0f766e", logo: null, supportEmail: "support@northwind.example.com", builtIn: false };

describe("white label", () => {
  it("shows the brand of the domain on the sign-in page", async () => {
    const api = panelApi("owner");
    api.on("GET /brand", () => NORTHWIND);
    render(<MemoryRouter initialEntries={["/login"]}><App /></MemoryRouter>);
    await waitFor(() => expect(document.title).toBe("Northwind Hosting"));
    expect(screen.getAllByText("Northwind Hosting").length).toBeGreaterThan(0);
    expect(screen.queryByText("cloudfactory")).toBeNull();
    expect(document.documentElement.style.getPropertyValue("--primary")).toBe("#0f766e");
  });

  it("shows the brand of the reseller to the reseller, with no word of the panel brand", async () => {
    await signedIn("reseller");
    render(<MemoryRouter initialEntries={["/customers"]}><App /></MemoryRouter>);
    expect(nav().getAllByText("Northwind Hosting").length).toBeGreaterThan(0);
    expect(nav().queryByText("cloudfactory")).toBeNull();
    // The logo of the reseller is in the menu.
    expect(document.querySelector(".sidebar .brand-logo")?.getAttribute("src")).toMatch(/^data:image\/png;base64,/);
    expect(document.documentElement.style.getPropertyValue("--primary")).toBe("#0f766e");
  });

  it("shows the brand of a reseller to a customer of the reseller", async () => {
    const api = panelApi("owner");
    api.change<Record<string, unknown>>("/me", (me) => { me.brand = { ...NORTHWIND, source: "reseller", nameServers: [], mailHost: null }; });
    await signIn("owner@example.com", "password");
    render(<MemoryRouter initialEntries={["/websites"]}><App /></MemoryRouter>);
    expect(nav().getAllByText("Northwind Hosting").length).toBeGreaterThan(0);
    expect(nav().getByRole("link", { name: "Help: support@northwind.example.com" }).getAttribute("href")).toBe("mailto:support@northwind.example.com");
    expect(document.body.textContent).not.toMatch(/cloudfactory/i);
  });

  it("lets a reseller change its brand and its panel domain", async () => {
    const api = await signedIn("reseller");
    const me = getState().customers.find((c) => c.role === "reseller")!;
    api.on("PATCH /customers/*", (body) => ({ id: me.id, ...(body as object) }));
    render(<MemoryRouter initialEntries={["/brand"]}><App /></MemoryRouter>);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Brand");
    expect(screen.getByText("Valid")).toBeTruthy();

    // A light colour is hard to read with white text: the form does not save it.
    fireEvent.change(screen.getByLabelText("Colour"), { target: { value: "#ffd700" } });
    expect(screen.getByText(/Select a darker colour/)).toBeTruthy();
    expect((screen.getAllByRole("button", { name: "Save" })[0] as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Colour"), { target: { value: "#7c2d12" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Northwind" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Save" })[0]);
    await waitFor(() => expect(api.changes.some((c) => c.method === "PATCH")).toBe(true));
    const change = api.changes.find((c) => c.method === "PATCH")!;
    expect(change.path).toBe(`/customers/${me.id}`);
    expect((change.body as { branding: Record<string, unknown> }).branding).toMatchObject({ name: "Northwind", colour: "#7c2d12" });

    // The API asks for a proof of control of a new panel domain: the page shows the TXT record.
    api.on("PATCH /customers/*", () => [422, { message: "x", errors: { "branding.panelDomain": ["Prove that you control this domain. Add a TXT record _cloudfactory-challenge.panel.new.example with the value cf-abc, and then save again. DNS changes can take some minutes."] } }]);
    fireEvent.change(screen.getByLabelText("Domain"), { target: { value: "panel.new.example" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Save" })[1]);
    await waitFor(() => expect(screen.getByLabelText("Value of the TXT record").textContent).toBe("cf-abc"));
  });

  it("lets the administrator set the default brand in Settings", async () => {
    const api = await signedIn("administrator");
    api.on("GET /brand/default", () => ({ name: "Cloudfactory", colour: "#1d5fd6", logo: null, supportEmail: null }));
    api.on("PUT /brand/default", (body) => ({ name: "Hostco", colour: "#1d5fd6", logo: null, supportEmail: null, ...(body as object) }));
    render(<MemoryRouter initialEntries={["/settings/brand"]}><App /></MemoryRouter>);
    await waitFor(() => expect(screen.getByLabelText("Name")).toBeTruthy());
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Hostco" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.changes.find((c) => c.method === "PUT")?.body).toMatchObject({ name: "Hostco", colour: null, logo: null }));
  });

  it("measures the contrast of white text as WCAG does", () => {
    expect(contrastWithWhite("#000000")).toBeCloseTo(21, 0);
    expect(contrastWithWhite("#1d5fd6")).toBeGreaterThan(4.5);
    expect(contrastWithWhite("#ffd700")).toBeLessThan(4.5);
  });
});
