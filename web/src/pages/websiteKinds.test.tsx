// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { reset, signedIn, website } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

describe("a website with no public address", () => {
  it("asks a worker for a name and a start command, not for a domain", async () => {
    await signedIn("owner");
    render(<MemoryRouter initialEntries={["/websites/new"]}><App /></MemoryRouter>);
    expect(await screen.findByLabelText("Primary domain")).toBeTruthy();
    expect(screen.queryByLabelText("Start command")).toBeNull();

    fireEvent.click(screen.getByRole("radio", { name: /Private website/ }));
    expect(screen.queryByLabelText("Primary domain")).toBeNull();
    expect(screen.getByLabelText("Name")).toBeTruthy();
    expect(screen.queryByLabelText("Start command")).toBeNull();

    fireEvent.click(screen.getByRole("radio", { name: /Background worker/ }));
    expect(screen.getByLabelText("Start command")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Create worker" })).toBeTruthy();
  });

  it("shows a private website with its network name and with no domains", async () => {
    await signedIn("owner");
    const site = website("orders-1c16.internal");
    render(<MemoryRouter initialEntries={[`/websites/${site.id}/settings`]}><App /></MemoryRouter>);
    expect((await screen.findAllByText("Private website")).length).toBeGreaterThan(0);
    expect(screen.getByText("orders-1c16.internal:80")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Visit site/ })).toBeNull();
    expect(screen.queryByRole("link", { name: "Domains" })).toBeNull();
  });

  it("shows the start command of a worker, and no network name", async () => {
    await signedIn("owner");
    const site = website("queue-00b1.internal");
    render(<MemoryRouter initialEntries={[`/websites/${site.id}/runtime`]}><App /></MemoryRouter>);
    const input = await screen.findByLabelText("Command that the worker runs") as HTMLInputElement;
    expect(input.value).toBe("php artisan queue:work --tries=3");
    expect(screen.queryByText("Worker mode")).toBeNull();
    expect(screen.queryByText("Document root")).toBeNull();
  });
});
