// Settings and Logs need room the side panel doesn't have (a settings
// rail, wide log lines), so from the peek they open in the full page by
// default. One remembered choice per browser; "Use side panel" on the toast
// that announces the jump turns it off.

const KEY = "kuso.peek.complex-tabs";

export const COMPLEX_TABS: ReadonlySet<string> = new Set(["settings", "logs"]);

export function complexTabsInPage(): boolean {
  try {
    return window.localStorage.getItem(KEY) !== "panel";
  } catch {
    return true;
  }
}

export function setComplexTabsInPage(inPage: boolean): void {
  try {
    window.localStorage.setItem(KEY, inPage ? "page" : "panel");
  } catch {
    // Storage blocked: the default (full page) stays.
  }
}

// opensInPage: would picking this tab from the peek go to the full page?
export function opensInPage(tab: string | undefined): boolean {
  return !!tab && COMPLEX_TABS.has(tab) && complexTabsInPage();
}
