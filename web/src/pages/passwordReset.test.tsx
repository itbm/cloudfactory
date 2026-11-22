// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { panelApi, reset } from "../test/panelApi";

afterEach(() => {
  cleanup();
  reset();
});

const SENT = "If a user has this email, the panel sends a link to it. The link works for 60 minutes. Look in your spam folder also.";

describe("password reset by email", () => {
  it("shows the link on the sign-in page only when the panel can send email", async () => {
    const api = panelApi("owner");
    api.on("GET /auth/methods", () => ({ passwordReset: false }));
    render(<MemoryRouter initialEntries={["/login"]}><App /></MemoryRouter>);
    await screen.findByRole("heading", { name: "Sign in" });
    await waitFor(() => expect(api.changes).toEqual([]));
    expect(screen.queryByRole("link", { name: "Forgot your password?" })).toBeNull();
    cleanup();

    panelApi("owner");
    render(<MemoryRouter initialEntries={["/login"]}><App /></MemoryRouter>);
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "owner@example.com" } });
    fireEvent.click(await screen.findByRole("link", { name: "Forgot your password?" }));
    // The form keeps the email that the user typed on the sign-in page.
    expect(await screen.findByRole("heading", { name: "Reset your password" })).toBeTruthy();
    expect((screen.getByLabelText("Email") as HTMLInputElement).value).toBe("owner@example.com");
  });

  it("sends the link and gives the same answer for each email", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/password-reset", () => [202, { message: SENT }]);
    render(<MemoryRouter initialEntries={["/forgot-password"]}><App /></MemoryRouter>);
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "nobody@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Send the link" }));
    expect(await screen.findByRole("heading", { name: "Look in your mailbox" })).toBeTruthy();
    expect(screen.getByText(SENT)).toBeTruthy();
    expect(api.changes).toEqual([{ method: "POST", path: "/auth/password-reset", body: { email: "nobody@example.com" } }]);
  });

  it("sets a new password with the code from the link, and removes the code from the address", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/password-reset/confirm", () => [204, null]);
    render(<MemoryRouter initialEntries={["/reset-password#email=owner%40example.com&token=abc123"]}><App /></MemoryRouter>);
    expect(screen.getByText("owner@example.com", { selector: "strong" })).toBeTruthy();
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "a-new-long-password" } });
    fireEvent.change(screen.getByLabelText("Type the new password again"), { target: { value: "a-new-long-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Set password" }));
    expect(await screen.findByRole("heading", { name: "Your password is changed" })).toBeTruthy();
    expect(api.changes).toEqual([{ method: "POST", path: "/auth/password-reset/confirm", body: { email: "owner@example.com", token: "abc123", password: "a-new-long-password", password_confirmation: "a-new-long-password" } }]);

    // The user signs in with the new password. The sign-in page has the email.
    fireEvent.click(screen.getByRole("link", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeTruthy();
    expect((screen.getByLabelText("Email") as HTMLInputElement).value).toBe("owner@example.com");
  });

  it("shows the error of a link that does not work, with a link to ask again", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/password-reset/confirm", () => [422, { message: "x", errors: { token: ["This link does not work. It works one time only, for 60 minutes. Ask for a new link."] } }]);
    render(<MemoryRouter initialEntries={["/reset-password#email=owner%40example.com&token=old"]}><App /></MemoryRouter>);
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "a-new-long-password" } });
    fireEvent.change(screen.getByLabelText("Type the new password again"), { target: { value: "a-new-long-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Set password" }));
    expect(await screen.findByRole("heading", { name: "This link does not work" })).toBeTruthy();
    expect(screen.getByText(/It works one time only, for 60 minutes/)).toBeTruthy();
    expect(screen.queryByLabelText("New password")).toBeNull();
    fireEvent.click(screen.getByRole("link", { name: "Ask for a new link" }));
    expect(await screen.findByRole("heading", { name: "Reset your password" })).toBeTruthy();
    expect((screen.getByLabelText("Email") as HTMLInputElement).value).toBe("owner@example.com");
  });

  it("tells the user when the link is not complete", () => {
    panelApi("owner");
    render(<MemoryRouter initialEntries={["/reset-password"]}><App /></MemoryRouter>);
    expect(screen.getByText(/This link is not complete/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Ask for a new link" }).getAttribute("href")).toBe("/forgot-password");
  });
});
