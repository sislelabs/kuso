import { smoothScroll } from "@/lib/reduced-motion";

const FLASH_MS = 1600;

// revealSettingsTarget scrolls a settings target into view and moves
// keyboard focus there, so the next Tab continues from what's on screen.
// A section id focuses its heading; a field anchor or control id focuses
// the control and briefly highlights its field. Returns false when the
// target isn't rendered (yet).
export function revealSettingsTarget(id: string): boolean {
  const el = document.getElementById(id);
  if (!el) return false;
  const field = el.closest<HTMLElement>("[data-settings-field]");
  const target = field ?? el;
  target.scrollIntoView({ behavior: smoothScroll(), block: field ? "center" : "start" });
  if (!field) {
    el.querySelector<HTMLElement>("[data-section-heading]")?.focus({ preventScroll: true });
    return true;
  }
  const control =
    (el.matches("input, textarea, select, button") ? el : null) ??
    field.querySelector<HTMLElement>("input:not([type=hidden]):not(:disabled), textarea, select, button[role=switch], button");
  control?.focus({ preventScroll: true });
  field.dataset.flash = "true";
  window.setTimeout(() => {
    delete field.dataset.flash;
  }, FLASH_MS);
  return true;
}

export interface SettingsFieldHit {
  // Element id revealSettingsTarget can take: the control's id, else
  // the field's anchor id.
  target: string;
  label: string;
  section: string;
}

// findSettingsFields searches the rendered field labels, so search
// results always point at where a field actually is.
export function findSettingsFields(root: HTMLElement, query: string): SettingsFieldHit[] {
  const q = query.trim().toLowerCase();
  if (q.length < 2) return [];
  const out: SettingsFieldHit[] = [];
  root.querySelectorAll<HTMLElement>("[data-settings-field]").forEach((f) => {
    const label = f.querySelector<HTMLElement>("[data-field-label]")?.textContent?.trim() ?? "";
    const section = f.closest("section")?.id ?? "";
    const control = f.dataset.settingsField || f.id;
    if (!label || !section || !control || !label.toLowerCase().includes(q)) return;
    out.push({ target: control, label, section });
  });
  return out;
}
