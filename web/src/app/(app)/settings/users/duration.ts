// toGoDuration rewrites day units ("7d", "1d12h") into hours, because the
// server parses expiresIn with Go's time.ParseDuration, which has no "d".
// Anything else is passed through for the server to validate.
export function toGoDuration(input: string): string {
  const v = input.trim();
  const m = v.match(/^(\d+)d(.*)$/);
  if (!m) return v;
  const hours = Number(m[1]) * 24;
  const rest = m[2];
  const restHours = rest.match(/^(\d+)h(.*)$/);
  if (restHours) return `${hours + Number(restHours[1])}h${restHours[2]}`;
  return `${hours}h${rest}`;
}
