// errorTitle is an error group's message as a heading: the leading log
// timestamp and level the app printed ("2026-11-08T19:40:05Z ERROR …")
// are noise there, since the row already says when and that it's an error.
const LEADING_TS = /^\[?\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?\]?\s*/;
const LEADING_LEVEL = /^\[?(?:ERROR|ERR|FATAL|PANIC|CRIT(?:ICAL)?|E)\]?(?::\s*|\s+)/i;

export function errorTitle(message: string, sample?: string): string {
  const once = message.trim().replace(LEADING_TS, "");
  const stripped = once.replace(LEADING_LEVEL, "").trim();
  // A traceback header names nothing; the exception line closing the
  // trace does. (Newer servers already store that line as the message.)
  if (stripped.startsWith("Traceback (most recent call last)")) return pythonException(sample) ?? "Python traceback";
  return stripped || message.trim();
}

function pythonException(sample: string | undefined): string | undefined {
  const lines = (sample ?? "").split("\n").map((l) => l.trimEnd());
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i];
    if (!l || /^\s/.test(l) || l.startsWith("Traceback") || l.startsWith("During handling")) continue;
    if (/^[A-Za-z_][\w.]*(?::\s|:?$)/.test(l)) return l;
  }
  return undefined;
}
