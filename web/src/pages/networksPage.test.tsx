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

async function open(role: Role): Promise<PanelApi> {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={["/networks"]}><App /></MemoryRouter>);
  return api;
}
const network = (name: string) => getState().networks.find((n) => n.name === name && n.customerId === website("acme.example.com").customerId)!;

describe("the page of the private networks", () => {
  it("shows an owner the networks of the customer with their members", async () => {
    await open("owner");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Private networks");
    const main = screen.getByRole("main");
    expect(main.textContent).toContain("Acme Ltd has 1 of 3 named networks.");
    const row = within(main).getByText("reports").closest("tr")!;
    // The members show their network names.
    expect(row.textContent).toContain(website("storefront.acme.example.com").networkHost);
    expect(row.textContent).toContain("mariadb-reports.internal");
    expect(within(main).getByText("default").closest("tr")!.textContent).toContain("Default");
  });

  it("makes a named network with the members that the user selects", async () => {
    const api = await open("owner");
    const shop = website("shop.acme.example.com");
    api.on("POST /networks", (body) => [201, { id: "new-network", customerId: shop.customerId, isDefault: false, ...(body as object) }]);
    fireEvent.click(screen.getByRole("button", { name: "New network" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: " Backend " } });
    fireEvent.click(within(dialog).getByRole("checkbox", { name: new RegExp(`^${shop.name}`) }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Create network" }));
    await waitFor(() => expect(api.changes).toHaveLength(2));
    expect(api.changes[1]).toEqual({ method: "POST", path: "/networks", body: { name: "backend", customerId: shop.customerId, websiteIds: [shop.id], databaseServerIds: [] } });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("changes the members of the network default, and does not rename it", async () => {
    const api = await open("owner");
    const base = network("default");
    api.on("PATCH /networks/*", () => [200, base]);
    const row = within(screen.getByRole("main")).getByText("default").closest("tr")!;
    fireEvent.click(within(row).getByRole("button", { name: "Actions for the network default" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Change members" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByLabelText("Name")).toBeNull();
    const shop = website("shop.acme.example.com");
    fireEvent.click(within(dialog).getByRole("checkbox", { name: new RegExp(`^${shop.name}`) }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save network" }));
    await waitFor(() => expect(api.changes).toHaveLength(2));
    expect(api.changes[1]).toMatchObject({ method: "PATCH", path: `/networks/${base.id}` });
    const body = api.changes[1].body as { name?: string; websiteIds: string[]; databaseServerIds: string[] };
    expect(body.name).toBeUndefined();
    expect(body.websiteIds).not.toContain(shop.id);
    expect(body.websiteIds).toHaveLength(base.websiteIds.length - 1);
    expect(body.databaseServerIds).toEqual(base.databaseServerIds);
  });

  it("shows the message of the API when the plan allows no more networks", async () => {
    const api = await open("owner");
    api.on("POST /networks", () => [422, { message: "x", errors: { name: ["The plan of the customer allows 3 named networks."] } }]);
    fireEvent.click(screen.getByRole("button", { name: "New network" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "more" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create network" }));
    await waitFor(() => expect(dialog.textContent).toContain("The plan of the customer allows 3 named networks."));
  });

  it("gives no button to a customer with a plan that has only the network default", async () => {
    const api = await signedIn("owner");
    api.change<{ data: { networks: number }[] }>("/plans", (b) => { for (const p of b.data) p.networks = 0; });
    const { refresh } = await import("../api/store");
    await refresh();
    render(<MemoryRouter initialEntries={["/networks"]}><App /></MemoryRouter>);
    expect(screen.getByRole("main").textContent).toContain('has only the network "default"');
    expect(screen.queryByRole("button", { name: "New network" })).toBeNull();
  });
});
