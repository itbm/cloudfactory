import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import type { Role } from "../api/types";
import { reset, signedIn, type PanelApi } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

async function open(role: Role, path: string): Promise<PanelApi> {
  const api = await signedIn(role);
  render(<MemoryRouter initialEntries={[path]}><App /></MemoryRouter>);
  return api;
}

describe("the email notifications", () => {
  it("lets a user turn off an event, but not a security email", async () => {
    const api = await open("owner", "/settings/notifications");
    api.on("PUT /me/notification-preferences", () => ({ data: [] }));
    const main = screen.getByRole("main");
    const failed = await within(main).findByRole("switch", { name: "Email for Failed jobs" });
    expect(failed.getAttribute("aria-checked")).toBe("true");
    expect(within(main).getByRole("switch", { name: "Email for Sign-in from a new device" }).hasAttribute("disabled")).toBe(true);
    // An owner does not see the events of administrators, nor the email settings.
    expect(within(main).queryByText("Servers that are offline")).toBeNull();
    expect(screen.queryByRole("button", { name: "Email delivery" })).toBeNull();

    fireEvent.click(failed);
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "PUT", path: "/me/notification-preferences", body: { events: { "job.failed": false } } }));
    // The emails of the account of the owner.
    expect(await within(main).findByText("Emails to your account")).toBeTruthy();
    expect(main.textContent).toContain("Failed: Create database");
  });

  it("lets an administrator set the SMTP server and send a test email", async () => {
    const api = await open("administrator", "/settings/email");
    const main = screen.getByRole("main");
    const host = await within(main).findByLabelText("Host") as HTMLInputElement;
    await waitFor(() => expect(host.value).toBe("smtp.mail.example"));
    expect(within(main).getByText("Works")).toBeTruthy();
    api.on("PUT /mail-server", (body) => ({ configured: true, enabled: true, hasPassword: true, panelFallback: null, testedAt: null, testError: null, ...(body as object) }));
    fireEvent.change(host, { target: { value: "smtp.example.net" } });
    fireEvent.click(within(main).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.changes.at(-1)?.path).toBe("/mail-server"));
    // The password stays when the field is empty.
    expect(api.changes.at(-1)?.body).toMatchObject({ host: "smtp.example.net", port: 587, encryption: "starttls" });
    expect((api.changes.at(-1)?.body as Record<string, unknown>).password).toBeUndefined();

    api.on("POST /mail-server/test", () => ({ sent: false, to: "ops@example.com", error: "Connection refused", server: { configured: true, enabled: true, host: "smtp.example.net", port: 587, encryption: "starttls", hasPassword: true, testError: "Connection refused" } }));
    fireEvent.change(within(main).getByLabelText("Send to"), { target: { value: "ops@example.com" } });
    fireEvent.click(within(main).getByRole("button", { name: "Send test email" }));
    expect(await within(main).findByText("The server did not accept the email.")).toBeTruthy();
    expect(api.changes.at(-1)).toEqual({ method: "POST", path: "/mail-server/test", body: { to: "ops@example.com" } });
  });

  it("edits a template with a preview and adds a value at the cursor", async () => {
    const api = await open("reseller", "/settings/email");
    api.on("POST /notification-templates/*/preview", (body) => ({ subject: (body as { subject: string }).subject, text: "x", html: "<p>x</p>" }));
    api.on("PUT /notification-templates/*", (body) => ({ event: "job.failed", label: "Failed jobs", description: "", changed: true, originalSubject: "", originalBody: "", variables: [], ...(body as object) }));
    const main = screen.getByRole("main");
    const row = (await within(main).findByText("Failed jobs")).closest("tr")!;
    // The reseller changed this template in the sample data.
    expect(row.textContent).toContain("Changed");
    fireEvent.click(within(row).getByRole("button", { name: "Edit" }));
    const dialog = screen.getByRole("dialog");
    const subject = within(dialog).getByLabelText("Subject") as HTMLInputElement;
    fireEvent.change(subject, { target: { value: "Problem with {{website}}" } });
    const text = within(dialog).getByLabelText("Text") as HTMLTextAreaElement;
    fireEvent.change(text, { target: { value: "Hello " } });
    text.setSelectionRange(6, 6);
    fireEvent.click(within(dialog).getByRole("button", { name: "{{name}}" }));
    expect(text.value).toBe("Hello {{name}}");
    expect(await within(dialog).findByTitle("Preview of the email")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save template" }));
    await waitFor(() => expect(api.changes.at(-1)).toEqual({ method: "PUT", path: "/notification-templates/job.failed", body: { subject: "Problem with {{website}}", body: "Hello {{name}}" } }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
