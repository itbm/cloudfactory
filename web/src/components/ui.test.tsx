// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { Modal, Tabs } from "./ui";

afterEach(cleanup);

/** A dialog as the pages make it: each render gives a new onClose function. */
function Dialog() {
  const [open, setOpen] = useState(true);
  const [form, setForm] = useState({ name: "", email: "" });
  if (!open) return <p>Closed</p>;
  return (
    <Modal title="Add customer" onClose={() => setOpen(false)}>
      <label>Name<input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></label>
      <label>Email<input value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} /></label>
    </Modal>
  );
}

describe("Modal", () => {
  it("puts the focus in the first field when it opens", () => {
    render(<Dialog />);
    expect(document.activeElement).toBe(screen.getByLabelText("Name"));
  });

  it("keeps the focus in the field while the user types", () => {
    render(<Dialog />);
    const email = screen.getByLabelText<HTMLInputElement>("Email");
    act(() => email.focus());
    // Each change renders the parent again, with a new onClose function.
    for (const value of ["a", "an", "ann"]) {
      fireEvent.change(email, { target: { value } });
      expect(document.activeElement).toBe(email);
    }
    expect(email.value).toBe("ann");
    expect(screen.getByLabelText<HTMLInputElement>("Name").value).toBe("");
  });

  it("closes with the Escape key after the parent rendered again", () => {
    render(<Dialog />);
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "a" } });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByText("Closed")).toBeTruthy();
  });
});

describe("Tabs", () => {
  it("marks the selected tab", () => {
    render(<Tabs label="Sections" current="users" items={[{ id: "customers", label: "Customers" }, { id: "users", label: "Users" }]} />);
    expect(screen.getByRole("button", { name: "Users" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("button", { name: "Customers" }).getAttribute("aria-current")).toBeNull();
  });
});
