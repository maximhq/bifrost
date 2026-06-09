"use client";

import { HeadersTable } from "@/components/ui/headersTable";

// ---------- MetadataTableEditor ----------

function parseMetadataValue(metadataJson: string): Record<string, string> {
  if (!metadataJson.trim()) {
    return {};
  }

  try {
    const parsed = JSON.parse(metadataJson) as unknown;
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      return {};
    }

    return Object.fromEntries(
      Object.entries(parsed as Record<string, unknown>).map(([key, value]) => [
        key,
        String(value ?? ""),
      ]),
    );
  } catch {
    return {};
  }
}

function serializeMetadataValue(value: Record<string, string>): string {
  const validEntries = Object.entries(value).filter(([key]) => key.trim());
  if (validEntries.length === 0) {
    return "";
  }

  return JSON.stringify(Object.fromEntries(validEntries), null, 2);
}

export function MetadataTableEditor({
  metadataJson,
  onChange,
  error,
}: {
  metadataJson: string;
  onChange: (json: string) => void;
  error?: string;
}) {
  return (
    <div className="space-y-2">
      <HeadersTable
        label=""
        value={parseMetadataValue(metadataJson)}
        onChange={(next) => onChange(serializeMetadataValue(next))}
        keyPlaceholder="Metadata key"
        valuePlaceholder="Metadata value"
      />
      {error && (
        <p className="text-destructive text-xs mt-1" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
