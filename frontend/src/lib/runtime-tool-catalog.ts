export interface RuntimeToolParameter {
  name: string;
  type: string;
  required: boolean;
  description: string;
}

const groupLabels: Record<string, string> = {
  file: "File",
  chunked_write: "Chunked Write",
  shell: "Shell",
  search: "Search",
  skill: "Skill",
  memory: "Memory",
  session_state: "Session State",
  observable: "Observable",
};

export function runtimeToolGroupLabel(group: unknown): string {
  if (typeof group !== "string" || group.trim() === "") return "Other";
  const normalized = group.trim().toLowerCase();
  if (groupLabels[normalized]) return groupLabels[normalized];
  return normalized
    .split(/[_\s-]+/)
    .filter(Boolean)
    .map((part) => part[0].toUpperCase() + part.slice(1))
    .join(" ");
}

export function runtimeToolTimeoutLabel(timeout: unknown): string {
  if (!isRecord(timeout)) return "unknown timeout";
  const mode = safeGet(timeout, "mode");
  const seconds = safeGet(timeout, "seconds");
  if (mode === "disabled") return "tool managed";
  if (
    mode === "bounded" &&
    typeof seconds === "number" &&
    Number.isFinite(seconds) &&
    seconds > 0
  ) {
    return `${seconds}s timeout`;
  }
  return "unknown timeout";
}

export function runtimeToolParameters(schema: unknown): RuntimeToolParameter[] {
  try {
    if (!isRecord(schema)) return [];
    const properties = safeGet(schema, "properties");
    if (!isRecord(properties)) return [];
    const requiredValue = safeGet(schema, "required");
    const required = new Set(
      Array.isArray(requiredValue)
        ? requiredValue.filter((name): name is string => typeof name === "string")
        : [],
    );

    return Object.keys(properties)
      .sort()
      .map((name) => {
        const property = safeGet(properties, name);
        const description = isRecord(property)
          ? safeGet(property, "description")
          : undefined;
        return {
          name,
          type: schemaTypeLabel(property),
          required: required.has(name),
          description: typeof description === "string" ? description : "",
        };
      });
  } catch {
    return [];
  }
}

export function formatRuntimeToolSchema(schema: unknown): string {
  try {
    return JSON.stringify(normalizeSchemaValue(schema, new WeakSet()), null, 2);
  } catch {
    return JSON.stringify("[Unable to format schema]");
  }
}

function schemaTypeLabel(
  schema: unknown,
  seen: WeakSet<object> = new WeakSet(),
): string {
  try {
    if (!isRecord(schema)) return "unknown";
    if (seen.has(schema)) return "unknown";
    seen.add(schema);
    try {
      const oneOf = safeGet(schema, "oneOf");
      if (Array.isArray(oneOf)) return unionTypeLabel(oneOf, seen);
      const anyOf = safeGet(schema, "anyOf");
      if (Array.isArray(anyOf)) return unionTypeLabel(anyOf, seen);

      const typeValue = safeGet(schema, "type");
      let label = declaredTypeLabel(typeValue, schema, seen);
      if (label === "unknown") {
        if (isRecord(safeGet(schema, "properties"))) label = "object";
        else if (safeGet(schema, "items") !== undefined) {
          label = arrayTypeLabel(schema, seen);
        }
      }

      const enumValue = safeGet(schema, "enum");
      if (Array.isArray(enumValue) && enumValue.length > 0) {
        const values = enumValue.map(formatEnumValue).join(" | ");
        return label === "unknown" ? `enum (${values})` : `${label} enum (${values})`;
      }
      return label;
    } finally {
      seen.delete(schema);
    }
  } catch {
    return "unknown";
  }
}

function unionTypeLabel(options: unknown[], seen: WeakSet<object>): string {
  const labels = options.map((option) => schemaTypeLabel(option, seen));
  const unique = labels.filter((label, index) => labels.indexOf(label) === index);
  return unique.length > 0 ? unique.join(" | ") : "unknown";
}

function declaredTypeLabel(
  typeValue: unknown,
  schema: Record<string, unknown>,
  seen: WeakSet<object>,
): string {
  const types = Array.isArray(typeValue) ? typeValue : [typeValue];
  const labels = types
    .filter(
      (value): value is string =>
        typeof value === "string" && value.trim() !== "",
    )
    .map((value) => value.trim())
    .map((value) => (value === "array" ? arrayTypeLabel(schema, seen) : value));
  const unique = labels.filter((value, index) => labels.indexOf(value) === index);
  return unique.length > 0 ? unique.join(" | ") : "unknown";
}

function arrayTypeLabel(
  schema: Record<string, unknown>,
  seen: WeakSet<object>,
): string {
  const items = safeGet(schema, "items");
  const itemLabel = Array.isArray(items)
    ? unionTypeLabel(items, seen)
    : schemaTypeLabel(items, seen);
  return `array<${itemLabel}>`;
}

function formatEnumValue(value: unknown): string {
  try {
    const json = JSON.stringify(value);
    return json === undefined ? String(value) : json;
  } catch {
    return "?";
  }
}

function normalizeSchemaValue(value: unknown, seen: WeakSet<object>): unknown {
  if (value === undefined) return "[Undefined]";
  if (typeof value === "bigint") return `[BigInt: ${value.toString()}]`;
  if (typeof value === "function") return "[Function]";
  if (typeof value === "symbol") return `[Symbol: ${value.description ?? ""}]`;
  if (typeof value === "number" && !Number.isFinite(value)) {
    return `[Number: ${String(value)}]`;
  }
  if (value === null || typeof value !== "object") return value;
  if (seen.has(value)) return "[Circular]";

  seen.add(value);
  try {
    if (Array.isArray(value)) {
      return value.map((item) => normalizeSchemaValue(item, seen));
    }
    const normalized = Object.create(null) as Record<string, unknown>;
    for (const key of Object.keys(value).sort()) {
      normalized[key] = normalizeSchemaValue(
        (value as Record<string, unknown>)[key],
        seen,
      );
    }
    return normalized;
  } finally {
    seen.delete(value);
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function safeGet(record: Record<string, unknown>, key: string): unknown {
  try {
    return record[key];
  } catch {
    return undefined;
  }
}
