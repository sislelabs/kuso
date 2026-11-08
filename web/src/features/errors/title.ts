// errorTitle is an error group's message as a heading: the leading log
// timestamp and level the app printed ("2026-11-08T19:40:05Z ERROR …")
// are noise there, since the row already says when and that it's an error.
const LEADING_TS = /^\[?\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?\]?\s*/;
const LEADING_LEVEL = /^\[?(?:ERROR|ERR|FATAL|PANIC|CRIT(?:ICAL)?|E)\]?(?::\s*|\s+)/i;

export function errorTitle(message: string): string {
  const once = message.trim().replace(LEADING_TS, "");
  const stripped = once.replace(LEADING_LEVEL, "").trim();
  return stripped || message.trim();
}
