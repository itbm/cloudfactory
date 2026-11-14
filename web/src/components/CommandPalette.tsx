import { Search } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useStore } from "../api/hooks";
import { search } from "../api/selectors";
import type { SearchResult } from "../api/types";

const KIND_LABEL: Record<SearchResult["kind"], string> = {
  website: "Website",
  domain: "Domain",
  job: "Job",
  server: "Server",
  image: "Image",
  customer: "Customer",
};

/** The search box (UI rule: ⌘K or Ctrl+K). It finds websites, domains and jobs. */
export function CommandPalette({ open, onClose, placeholder }: { open: boolean; onClose: () => void; placeholder: string }) {
  const state = useStore();
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listId = useId();

  useEffect(() => {
    if (open) {
      setQuery("");
      setActive(0);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  const results = useMemo(() => search(state, query), [state, query]);
  useEffect(() => setActive(0), [query]);

  if (!open) return null;

  const choose = (r: SearchResult | undefined) => {
    if (!r) return;
    onClose();
    navigate(r.href);
  };

  return (
    <div className="palette-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="palette" role="dialog" aria-modal="true" aria-label="Search">
        <div className="palette-input">
          <Search size={18} aria-hidden="true" />
          <input
            ref={inputRef}
            value={query}
            placeholder={placeholder}
            role="combobox"
            aria-expanded={results.length > 0}
            aria-controls={listId}
            aria-activedescendant={results[active] ? `${listId}-${active}` : undefined}
            aria-label="Search"
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") onClose();
              else if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((a) => Math.min(a + 1, results.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(a - 1, 0));
              } else if (e.key === "Enter") choose(results[active]);
            }}
          />
          <kbd>Esc</kbd>
        </div>
        {query.trim() && results.length === 0 ? (
          <p className="palette-empty">No results for “{query}”.</p>
        ) : (
          <ul className="palette-results" id={listId} role="listbox" aria-label="Results">
            {results.map((r, i) => (
              <li
                key={`${r.kind}-${r.label}`}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === active}
                onMouseEnter={() => setActive(i)}
                onClick={() => choose(r)}
              >
                <span className="kind">{KIND_LABEL[r.kind]}</span>
                <span>{r.label}</span>
                <span className="detail">{r.detail}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
