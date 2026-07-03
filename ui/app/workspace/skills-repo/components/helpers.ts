"use client";

import { SkillFileEntry } from "@/lib/types/skills";
import {
	validateDescription,
	validateExtraFrontmatter,
	validateMetadata,
	validateSkillForm,
	validateSkillMdBody,
	validateSkillName,
	validateVersion,
} from "@/lib/validators/skills";
import { useEffect, useState } from "react";

// ---------- Constants ----------

export const PAGE_SIZE = 25;

// ---------- Helpers ----------

export function formatDate(dateStr: string) {
	return new Date(dateStr).toLocaleString();
}

export function formatDateShort(dateStr: string) {
	return new Date(dateStr).toLocaleDateString();
}

export function formatFileSize(bytes: number): string {
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function yamlScalar(value: unknown): string {
	if (value === null) return "null";
	if (typeof value === "number" || typeof value === "boolean") return String(value);
	return JSON.stringify(String(value));
}

function yamlMetadataScalar(value: unknown): string {
	if (value === null) return "null";
	if (typeof value === "number" || typeof value === "boolean") return String(value);
	return String(value);
}

function yamlBlock(value: unknown, indent = 0): string[] {
	const pad = " ".repeat(indent);
	if (Array.isArray(value)) {
		if (value.length === 0) return [`${pad}[]`];
		return value.flatMap((item) => {
			if (item !== null && typeof item === "object") {
				const nested = yamlBlock(item, indent + 2);
				return [`${pad}-`, ...nested];
			}
			return [`${pad}- ${yamlScalar(item)}`];
		});
	}
	if (value !== null && typeof value === "object") {
		const entries = Object.entries(value as Record<string, unknown>);
		if (entries.length === 0) return [`${pad}{}`];
		return entries.flatMap(([key, item]) => {
			if (item !== null && typeof item === "object") {
				return [`${pad}${key}:`, ...yamlBlock(item, indent + 2)];
			}
			return [`${pad}${key}: ${yamlScalar(item)}`];
		});
	}
	return [`${pad}${yamlScalar(value)}`];
}

export function yamlField(key: string, value: unknown): string[] {
	if (value !== null && typeof value === "object") {
		return [`${key}:`, ...yamlBlock(value, 2)];
	}
	return [`${key}: ${yamlScalar(value)}`];
}

function yamlMetadataBlock(value: unknown, indent = 0): string[] {
	const pad = " ".repeat(indent);
	if (Array.isArray(value)) {
		if (value.length === 0) return [`${pad}[]`];
		return value.flatMap((item) => {
			if (item !== null && typeof item === "object") {
				const nested = yamlMetadataBlock(item, indent + 2);
				return [`${pad}-`, ...nested];
			}
			return [`${pad}- ${yamlMetadataScalar(item)}`];
		});
	}
	if (value !== null && typeof value === "object") {
		const entries = Object.entries(value as Record<string, unknown>);
		if (entries.length === 0) return [`${pad}{}`];
		return entries.flatMap(([key, item]) => {
			if (item !== null && typeof item === "object") {
				return [`${pad}${key}:`, ...yamlMetadataBlock(item, indent + 2)];
			}
			return [`${pad}${key}: ${yamlMetadataScalar(item)}`];
		});
	}
	return [`${pad}${yamlMetadataScalar(value)}`];
}

export function composeFrontmatter(data: {
	name: string;
	description: string;
	license: string;
	compatibility: string;
	allowed_tools: string;
	extra_frontmatter_json: string;
	metadata_json: string;
}): string {
	const lines: string[] = [];
	lines.push(...yamlField("name", data.name));
	lines.push(...yamlField("description", data.description));
	if (data.license) lines.push(...yamlField("license", data.license));
	if (data.compatibility) lines.push(...yamlField("compatibility", data.compatibility));
	if (data.allowed_tools) lines.push(...yamlField("allowed-tools", data.allowed_tools));

	// Extra frontmatter renders as top-level YAML, matching serve-time composition.
	if (data.extra_frontmatter_json.trim()) {
		try {
			const ef = JSON.parse(data.extra_frontmatter_json) as Record<string, unknown>;
			for (const [key, value] of Object.entries(ef)) {
				lines.push(...yamlField(key, value));
			}
		} catch {
			/* skip if invalid */
		}
	}

	// Metadata is always nested under the metadata: frontmatter key.
	if (data.metadata_json.trim()) {
		try {
			const md = JSON.parse(data.metadata_json) as Record<string, string>;
			if (Object.keys(md).length > 0) {
				if (typeof md === "object" && md !== null) {
					lines.push("metadata:", ...yamlMetadataBlock(md, 2));
				} else {
					lines.push(`metadata: ${yamlMetadataScalar(md)}`);
				}
			}
		} catch {
			/* skip if invalid */
		}
	}

	return `---\n${lines.join("\n")}\n---`;
}

export function useDebouncedValue<T>(value: T, delayMs: number): T {
	const [debouncedValue, setDebouncedValue] = useState(value);

	useEffect(() => {
		const timeout = window.setTimeout(() => setDebouncedValue(value), delayMs);
		return () => window.clearTimeout(timeout);
	}, [value, delayMs]);

	return debouncedValue;
}

export function parseJsonRecord(value: string): Record<string, unknown> | null {
	if (!value.trim()) return null;
	try {
		const parsed = JSON.parse(value) as unknown;
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
		const record = parsed as Record<string, unknown>;
		return Object.keys(record).length > 0 ? record : null;
	} catch {
		return null;
	}
}

export function formatYamlRecord(value: Record<string, unknown>): string {
	return Object.entries(value)
		.flatMap(([key, item]) => yamlField(key, item))
		.join("\n");
}

// ---------- Skill Form ----------

export interface SkillFormState {
	name: string;
	description: string;
	license: string;
	compatibility: string;
	allowedTools: string;
	extraFrontmatterJson: string;
	metadataJson: string;
	skillMdBody: string;
	version: string;
	files: SkillFileEntry[];
}

export function useSkillForm(initial?: SkillFormState) {
	const [name, setName] = useState(initial?.name ?? "");
	const [description, setDescription] = useState(initial?.description ?? "");
	const [license, setLicense] = useState(initial?.license ?? "");
	const [compatibility, setCompatibility] = useState(initial?.compatibility ?? "");
	const [allowedTools, setAllowedTools] = useState(initial?.allowedTools ?? "");
	const [extraFrontmatterJson, setExtraFrontmatterJson] = useState(initial?.extraFrontmatterJson ?? "");
	const [metadataJson, setMetadataJson] = useState(initial?.metadataJson ?? "");
	const [skillMdBody, setSkillMdBody] = useState(initial?.skillMdBody ?? "");
	const [version, setVersion] = useState(initial?.version ?? "1.0.0");
	const [files, setFiles] = useState<SkillFileEntry[]>(initial?.files ?? []);
	const [errors, setErrors] = useState<Record<string, string>>({});
	const [bodyWarning, setBodyWarning] = useState<string | null>(null);

	const validateField = (field: string, value: string) => {
		let err: string | null = null;
		if (field === "name") err = validateSkillName(value);
		else if (field === "description") err = validateDescription(value);
		else if (field === "version") err = validateVersion(value);
		else if (field === "extra_frontmatter") err = validateExtraFrontmatter(value);
		else if (field === "metadata") err = validateMetadata(value);

		setErrors((prev) => {
			const next = { ...prev };
			if (err) next[field] = err;
			else delete next[field];
			return next;
		});
	};

	useEffect(() => {
		const result = validateSkillMdBody(skillMdBody);
		setBodyWarning(result.warning);
		setErrors((prev) => {
			const next = { ...prev };
			if (result.error) next.skill_md_body = result.error;
			else delete next.skill_md_body;
			return next;
		});
	}, [skillMdBody]);

	const hasErrors = Object.keys(errors).length > 0;

	const getPayload = () => {
		let parsedMetadata: Record<string, string> | undefined;
		if (metadataJson.trim()) {
			try {
				parsedMetadata = JSON.parse(metadataJson) as Record<string, string>;
			} catch {
				/* skip */
			}
		}

		let parsedExtraFrontmatter: Record<string, unknown> | undefined;
		if (extraFrontmatterJson.trim()) {
			try {
				parsedExtraFrontmatter = JSON.parse(extraFrontmatterJson) as Record<string, unknown>;
			} catch {
				/* skip */
			}
		}

		return {
			name,
			description,
			license: license || undefined,
			compatibility: compatibility || undefined,
			metadata: parsedMetadata,
			extra_frontmatter: parsedExtraFrontmatter,
			allowed_tools: allowedTools || undefined,
			skill_md_body: skillMdBody,
			version,
			files: files.map(({ __local, ...file }) => file),
		};
	};

	const runValidation = () => {
		const validationErrors = validateSkillForm({
			name,
			description,
			version,
			skill_md_body: skillMdBody,
			extra_frontmatter_json: extraFrontmatterJson || undefined,
			metadata_json: metadataJson || undefined,
		});
		if (validationErrors.length > 0) {
			const errMap: Record<string, string> = {};
			for (const e of validationErrors) errMap[e.field] = e.message;
			setErrors(errMap);
			return false;
		}
		return true;
	};

	const addFile = (entry: SkillFileEntry) => setFiles((prev) => [...prev, entry]);
	const removeFile = (index: number) => setFiles((prev) => prev.filter((_, i) => i !== index));
	const updateFile = (index: number, updates: Partial<SkillFileEntry>) =>
		setFiles((prev) => prev.map((f, i) => (i === index ? { ...f, ...updates } : f)));

	const reset = (state: SkillFormState) => {
		setName(state.name);
		setDescription(state.description);
		setLicense(state.license);
		setCompatibility(state.compatibility);
		setAllowedTools(state.allowedTools);
		setExtraFrontmatterJson(state.extraFrontmatterJson);
		setMetadataJson(state.metadataJson);
		setSkillMdBody(state.skillMdBody);
		setVersion(state.version);
		setFiles(state.files);
		setErrors({});
	};

	return {
		name,
		setName,
		description,
		setDescription,
		license,
		setLicense,
		compatibility,
		setCompatibility,
		allowedTools,
		setAllowedTools,
		extraFrontmatterJson,
		setExtraFrontmatterJson,
		metadataJson,
		setMetadataJson,
		skillMdBody,
		setSkillMdBody,
		version,
		setVersion,
		files,
		addFile,
		removeFile,
		updateFile,
		errors,
		hasErrors,
		bodyWarning,
		validateField,
		getPayload,
		runValidation,
		reset,
	};
}

export type SkillFormReturn = ReturnType<typeof useSkillForm>;