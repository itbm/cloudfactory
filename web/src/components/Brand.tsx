// White label: the brand that each person sees. The administrator sets the default brand.
// The customers of a reseller see only the brand of the reseller: its name, logo and colour.
// Before sign-in, the panel shows the brand of the domain that the browser opened.

import { useEffect, useSyncExternalStore } from "react";
import { useStore } from "../api/hooks";
import { signInBrand } from "../api/live";
import type { Brand } from "../api/types";

export function Logo() {
  return (
    <svg width="28" height="28" viewBox="0 0 32 32" aria-hidden="true">
      <path d="M8 19a5 5 0 0 1 1.2-9.8A6.5 6.5 0 0 1 21.6 9.6 4.8 4.8 0 0 1 24 19Z" fill="#fff" />
      <path d="M5 21h22v6H5z" fill="#5b8def" />
      <path d="M8 23h3v2H8zm5 0h3v2h-3zm5 0h3v2h-3z" fill="#0f1b33" />
    </svg>
  );
}

/** The built-in brand. The panel shows it until the API answers. */
export const BUILT_IN: Brand = { name: "Cloudfactory", colour: "#1d5fd6", logo: null, supportEmail: null, builtIn: true };

// The brand of the sign-in page. The panel reads it one time.
let publicBrand: Brand | null = null;
let asked = false;
const listeners = new Set<() => void>();

function readPublicBrand() {
  if (asked) return;
  asked = true;
  signInBrand()
    .then((b) => { publicBrand = b; })
    .catch(() => { asked = false; })
    .finally(() => listeners.forEach((l) => l()));
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

/** The brand that the person sees: the brand of the user after sign-in, else the brand of the domain. */
export function useBrand(): Brand {
  const state = useStore();
  const shown = useSyncExternalStore(subscribe, () => publicBrand, () => publicBrand);
  const signedIn = state.session.signedIn && state.brand;
  useEffect(() => { if (!signedIn) readPublicBrand(); }, [signedIn]);
  return (signedIn ? state.brand : shown) ?? BUILT_IN;
}

/** Mixes a colour with white (amount 0 to 1) or, with a negative amount, with black. */
export function mix(hex: string, amount: number): string {
  const n = parseInt(hex.slice(1), 16);
  const target = amount >= 0 ? 255 : 0;
  const a = Math.abs(amount);
  const channel = (c: number) => Math.round(c + (target - c) * a).toString(16).padStart(2, "0");
  return `#${channel((n >> 16) & 255)}${channel((n >> 8) & 255)}${channel(n & 255)}`;
}

/** The colours of the panel for a brand colour. */
export function brandColours(colour: string): Record<string, string> {
  return {
    "--primary": colour,
    "--primary-hover": mix(colour, -0.15),
    "--primary-soft": mix(colour, 0.9),
    "--primary-border": mix(colour, 0.65),
  };
}

/** Applies the brand to the page: the colours, the title of the browser tab and the icon. */
export function BrandTheme() {
  const brand = useBrand();
  useEffect(() => {
    const root = document.documentElement;
    for (const [name, value] of Object.entries(brandColours(brand.colour))) root.style.setProperty(name, value);
    document.title = brand.name;
    const icon = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (icon) {
      icon.href = brand.logo ?? "/favicon.svg";
      icon.type = brand.logo ? brand.logo.slice(5, brand.logo.indexOf(";")) : "image/svg+xml";
    }
  }, [brand.colour, brand.name, brand.logo]);
  return null;
}

/**
 * The logo and the name of the brand. "nav" is for the dark menu, "light" for a light page
 * such as the sign-in page.
 */
export function BrandMark({ brand, tone = "nav" }: { brand: Brand; tone?: "nav" | "light" }) {
  if (brand.builtIn) {
    return (
      <>
        {tone === "nav" ? <Logo /> : <span style={{ background: "#0f1b33", borderRadius: 8, padding: 4, display: "inline-flex" }}><Logo /></span>}
        <span aria-hidden="true" style={{ color: tone === "nav" ? "#fff" : "var(--text)" }}>cloudfactory<span>.io</span></span>
      </>
    );
  }
  return (
    <>
      {brand.logo
        ? <img className="brand-logo" src={brand.logo} alt="" />
        : <span className="brand-initial" aria-hidden="true" style={{ background: brand.colour }}>{brand.name.trim().charAt(0).toUpperCase() || "?"}</span>}
      <span className="brand-name" aria-hidden="true" style={{ color: tone === "nav" ? "#fff" : "var(--text)" }}>{brand.name}</span>
    </>
  );
}
