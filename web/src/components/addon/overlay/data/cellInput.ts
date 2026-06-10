import type { SQLColumn, SQLCellValue } from "@/features/projects";

// Maps a pg column to the kind of input the grid editor should render, and
// turns the editor's string/checkbox state into the { value, isNull } wire
// shape. We deliberately do NOT coerce numbers/dates client-side — the
// server binds the value as a parameter and Postgres does the final type
// coercion via the column type. The client's only job is the NULL-vs-empty
// distinction and JSON-shape validation.

export type InputKind = "text" | "number" | "bool" | "json" | "enum";

export function inputKindFor(col: SQLColumn): InputKind {
  if (col.isEnum) return "enum";
  const t = col.udtName || col.dataType;
  if (/^(int2|int4|int8|numeric|float4|float8|money|serial|bigserial)$/i.test(t))
    return "number";
  if (/^bool$/i.test(t)) return "bool";
  if (/^(json|jsonb)$/i.test(t)) return "json";
  return "text";
}

// Validates a candidate edit. Returns an error string, or null when ok.
export function validateCell(
  col: SQLColumn,
  raw: string,
  isNull: boolean
): string | null {
  if (isNull) {
    if (!col.nullable) return `${col.name} is NOT NULL`;
    return null;
  }
  const kind = inputKindFor(col);
  if (kind === "json") {
    try {
      JSON.parse(raw);
    } catch {
      return "invalid JSON";
    }
  }
  if (kind === "number" && raw.trim() !== "" && Number.isNaN(Number(raw))) {
    return "not a number";
  }
  return null;
}

// Builds the wire value from editor state. The raw string is sent as-is for
// text/number/json/enum (server + pg coerce); bool is sent as a real boolean.
export function toCellValue(
  col: SQLColumn,
  raw: string,
  isNull: boolean
): SQLCellValue {
  if (isNull) return { value: null, isNull: true };
  if (inputKindFor(col) === "bool") {
    return { value: raw === "true", isNull: false };
  }
  return { value: raw, isNull: false };
}
