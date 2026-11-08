// Parser for a pasted/uploaded .env file on the create-service flow.
// Stricter than the Variables panel's lenient textarea: every line that
// isn't blank or a comment must parse, and a bad line is reported with
// its line number instead of being silently dropped.

export interface DotenvVar {
  name: string;
  value: string;
  // 1-based line the assignment starts on.
  line: number;
}

export interface DotenvError {
  line: number;
  message: string;
}

export interface DotenvResult {
  vars: DotenvVar[];
  errors: DotenvError[];
}

const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

// Mirrors envNameReserved in server-go/internal/projects/services_ops.go.
export function reservedEnvName(name: string): string | null {
  if (name === "PORT") return "PORT is set by kuso from the service's port";
  if (name === "HOSTNAME") return "HOSTNAME is set by Kubernetes from the pod name";
  if (name.startsWith("KUBERNETES_")) return "KUBERNETES_* is reserved for in-cluster API access";
  return null;
}

function unescapeDouble(s: string): string {
  return s.replace(/\\([nrt"\\$])/g, (_, c: string) =>
    c === "n" ? "\n" : c === "r" ? "\r" : c === "t" ? "\t" : c,
  );
}

// parseDotenv accepts KEY=VALUE lines with optional `export ` prefix,
// # comments, single quotes (literal), double quotes (\n, \", \\ escapes,
// may span lines) and unquoted values with a trailing ` # comment`.
// A repeated key is an error: the server rejects duplicates, and silently
// keeping one of two values is how wrong secrets ship.
export function parseDotenv(text: string): DotenvResult {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const vars: DotenvVar[] = [];
  const errors: DotenvError[] = [];
  const seen = new Map<string, number>();

  for (let i = 0; i < lines.length; i++) {
    const lineNo = i + 1;
    const raw = lines[i].replace(/^\uFEFF/, "");
    const trimmed = raw.trim();
    if (trimmed === "" || trimmed.startsWith("#")) continue;

    const body = trimmed.replace(/^export\s+/, "");
    const eq = body.indexOf("=");
    if (eq < 0) {
      errors.push({ line: lineNo, message: "expected KEY=VALUE" });
      continue;
    }
    const name = body.slice(0, eq).trim();
    if (!NAME_RE.test(name)) {
      errors.push({
        line: lineNo,
        message: name
          ? `"${name}" isn't a valid name (letters, digits and _; can't start with a digit)`
          : "missing name before =",
      });
      continue;
    }
    const reserved = reservedEnvName(name);
    if (reserved) {
      errors.push({ line: lineNo, message: reserved });
      continue;
    }

    let rest = body.slice(eq + 1).replace(/^[ \t]+/, "");
    let value: string;
    const quote = rest[0];
    if (quote === '"' || quote === "'") {
      // Find the closing quote, possibly on a later line.
      let acc = rest.slice(1);
      let close = findClose(acc, quote);
      let j = i;
      while (close < 0 && j + 1 < lines.length) {
        j++;
        acc += "\n" + lines[j];
        close = findClose(acc, quote);
      }
      if (close < 0) {
        errors.push({ line: lineNo, message: `unterminated ${quote === '"' ? "double" : "single"} quote` });
        i = j;
        continue;
      }
      const after = acc.slice(close + 1).trim();
      if (after !== "" && !after.startsWith("#")) {
        errors.push({ line: j + 1, message: `unexpected text after closing quote: ${after.slice(0, 20)}` });
        i = j;
        continue;
      }
      const inner = acc.slice(0, close);
      value = quote === '"' ? unescapeDouble(inner) : inner;
      i = j;
    } else {
      const hash = rest.search(/\s#/);
      if (hash >= 0) rest = rest.slice(0, hash);
      value = rest.trim();
    }

    const prev = seen.get(name);
    if (prev !== undefined) {
      errors.push({ line: lineNo, message: `${name} is already set on line ${prev}` });
      continue;
    }
    seen.set(name, lineNo);
    vars.push({ name, value, line: lineNo });
  }
  return { vars, errors };
}

function findClose(s: string, quote: string): number {
  for (let k = 0; k < s.length; k++) {
    if (quote === '"' && s[k] === "\\") {
      k++;
      continue;
    }
    if (s[k] === quote) return k;
  }
  return -1;
}

const SECRET_NAME_RE = /(SECRET|TOKEN|PASSWORD|PASSWD|PWD|PRIVATE|CREDENTIAL|API_?KEY|ACCESS_?KEY|_KEY$|^KEY$|DSN|DATABASE_URL|_URI$|AUTH|SALT|SIGNING|WEBHOOK)/i;

// looksSecret decides whether the preview masks a value. Name-based, plus
// a connection string carrying user:password@.
export function looksSecret(name: string, value: string): boolean {
  if (SECRET_NAME_RE.test(name)) return true;
  return /:\/\/[^/\s:@]+:[^/\s@]+@/.test(value);
}
