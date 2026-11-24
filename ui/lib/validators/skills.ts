// Frontend quick-check validators for Skills Repository
// These run before any network request to give instant feedback.

import i18n from "@/lib/i18n";

const T = { ns: "config" } as const;

export const MAX_SKILL_FILE_SIZE_BYTES = 50 * 1024 * 1024;
export const MAX_SKILL_FILE_SIZE_LABEL = "50 MB";

/** Reserved frontmatter keys that cannot be used in extra_frontmatter */
const RESERVED_FRONTMATTER_KEYS = new Set(["name", "description", "license", "compatibility", "metadata", "allowed-tools"]);

/** Skill name: lowercase alphanumeric + hyphens, 1-64 chars */
const SKILL_NAME_REGEX = /^[a-z0-9]+(-[a-z0-9]+)*$/;

/** Semver format: MAJOR.MINOR.PATCH with optional -suffix */
const SEMVER_REGEX = /^(\d+)\.(\d+)\.(\d+)(-[a-zA-Z0-9._-]+)?$/;

export interface ValidationError {
	field: string;
	message: string;
}

/** Validate skill name against Agent Skills spec */
export function validateSkillName(name: string): string | null {
	if (!name) return i18n.t("skillsRepo.validation.nameRequired", T);
	if (name.length > 64) return i18n.t("skillsRepo.validation.nameTooLong", T);
	if (!SKILL_NAME_REGEX.test(name)) {
		return i18n.t("skillsRepo.validation.nameFormat", T);
	}
	const reservedNames = ["all-skills", "all", "codex", "claude-code"];
	if (reservedNames.includes(name)) {
		return i18n.t("skillsRepo.validation.nameReserved", { ...T, name });
	}
	return null;
}

/** Validate description (hard limit 1024 chars) */
export function validateDescription(description: string): string | null {
	if (!description || !description.trim()) return i18n.t("skillsRepo.validation.descriptionRequired", T);
	if (description.length > 1024) return i18n.t("skillsRepo.validation.descriptionTooLong", { ...T, length: description.length });
	return null;
}

/** Validate version string as semver */
export function validateVersion(version: string): string | null {
	if (!version) return i18n.t("skillsRepo.validation.versionRequired", T);
	const match = SEMVER_REGEX.exec(version);
	if (!match) return i18n.t("skillsRepo.validation.versionFormat", T);
	const [, major, minor, patch] = match;
	if (parseInt(major) < 0 || parseInt(minor) < 0 || parseInt(patch) < 0) {
		return i18n.t("skillsRepo.validation.versionParts", T);
	}
	return null;
}

/**
 * Parse a semver string into its numeric parts.
 * Returns null if the string is not valid semver.
 */
function parseSemver(version: string): { major: number; minor: number; patch: number; suffix: string } | null {
	const match = SEMVER_REGEX.exec(version);
	if (!match) return null;
	return {
		major: parseInt(match[1]),
		minor: parseInt(match[2]),
		patch: parseInt(match[3]),
		suffix: match[4] || "",
	};
}

/**
 * Compare two semver versions. Returns >0 if a > b, <0 if a < b, 0 if equal (ignoring suffix).
 */
function compareSemverNumeric(
	a: { major: number; minor: number; patch: number },
	b: { major: number; minor: number; patch: number },
): number {
	if (a.major !== b.major) return a.major - b.major;
	if (a.minor !== b.minor) return a.minor - b.minor;
	return a.patch - b.patch;
}

/**
 * Find the highest version string from a list of version strings.
 * Returns the highest by semver comparison, or the first if none parse.
 */
export function findHighestVersion(versions: string[]): string {
	if (versions.length === 0) return "0.0.0";
	let highest = versions[0];
	let highestParsed = parseSemver(highest);
	for (let i = 1; i < versions.length; i++) {
		const parsed = parseSemver(versions[i]);
		if (!parsed) continue;
		if (!highestParsed || compareSemverNumeric(parsed, highestParsed) > 0) {
			highest = versions[i];
			highestParsed = parsed;
		}
	}
	return highest;
}

/**
 * Check whether `newVersion` is a valid bump over `previousVersion`.
 * A valid bump means the new version is strictly different via numeric parts or suffix.
 * Returns null if the bump is valid, or an error message if not.
 */
export function validateVersionBump(newVersion: string, previousVersion: string): string | null {
	const prev = parseSemver(previousVersion);
	const next = parseSemver(newVersion);
	if (!prev || !next) return null; // let the format validator handle bad format

	// Numeric bump is always valid
	if (
		next.major > prev.major ||
		(next.major === prev.major && next.minor > prev.minor) ||
		(next.major === prev.major && next.minor === prev.minor && next.patch > prev.patch)
	) {
		return null;
	}

	// Same numeric parts — a different suffix is also a valid bump
	if (next.major === prev.major && next.minor === prev.minor && next.patch === prev.patch && next.suffix !== prev.suffix) {
		return null;
	}

	return i18n.t("skillsRepo.validation.versionBump", {
		...T,
		previous: previousVersion,
		major: `${prev.major + 1}.x.x`,
		minor: `${prev.major}.${prev.minor + 1}.x`,
		patch: `${prev.major}.${prev.minor}.${prev.patch + 1}`,
	});
}

/** Validate extra_frontmatter keys don't conflict with reserved names */
export function validateExtraFrontmatter(json: string): string | null {
	if (!json || !json.trim()) return null;
	try {
		const parsed = JSON.parse(json);
		if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
			return i18n.t("skillsRepo.validation.extraFrontmatterObject", T);
		}
		const conflicts = Object.keys(parsed).filter((k) => RESERVED_FRONTMATTER_KEYS.has(k));
		if (conflicts.length > 0) {
			return i18n.t("skillsRepo.validation.reservedKeys", { ...T, keys: conflicts.join(", ") });
		}
		return null;
	} catch {
		return i18n.t("skillsRepo.validation.invalidJson", T);
	}
}

/** Validate metadata is a flat key-value object with string values */
export function validateMetadata(json: string): string | null {
	if (!json || !json.trim()) return null;
	try {
		const parsed = JSON.parse(json);
		if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
			return i18n.t("skillsRepo.validation.metadataObject", T);
		}
		for (const [key, value] of Object.entries(parsed)) {
			if (typeof key !== "string") return i18n.t("skillsRepo.validation.metadataKeys", T);
			if (typeof value !== "string") return i18n.t("skillsRepo.validation.metadataValue", { ...T, key });
		}
		return null;
	} catch {
		return i18n.t("skillsRepo.validation.invalidJson", T);
	}
}

/** Validate skill_md_body (non-empty required) */
export function validateSkillMdBody(body: string): {
	error: string | null;
	warning: string | null;
} {
	if (!body || !body.trim()) {
		return { error: i18n.t("skillsRepo.validation.bodyRequired", T), warning: null };
	}
	// Soft limit warning (no blocking)
	const warning = body.length > 50000 ? i18n.t("skillsRepo.validation.bodyLarge", T) : null;
	return { error: null, warning };
}

// Removed directory-specific file extension restrictions: skills can contain arbitrary
// top-level files and folders, and any supported source can live anywhere in the tree.
/** Validate file extension against directory constraints */
export function validateFileForDirectory(_filename: string, _directory: string): string | null {
	return null;
}

function utf8ByteLength(value: string): number {
	return new TextEncoder().encode(value).length;
}

function dataURLDecodedLength(value: string): number | null {
	const marker = ";base64,";
	const index = value.indexOf(marker);
	if (index < 0) return null;
	const payload = value.slice(index + marker.length).replace(/\s/g, "");
	if (!payload) return 0;
	const padding = payload.endsWith("==") ? 2 : payload.endsWith("=") ? 1 : 0;
	return Math.floor((payload.length * 3) / 4) - padding;
}

export function validateSkillFileSize(sizeBytes: number): string | null {
	if (sizeBytes > MAX_SKILL_FILE_SIZE_BYTES) {
		return i18n.t("skillsRepo.validation.fileTooLarge", { ...T, limit: MAX_SKILL_FILE_SIZE_LABEL });
	}
	return null;
}

/** Validate source type-specific fields */
export function validateSourceType(
	sourceType: string,
	values: {
		url?: string;
		filepath?: string;
		dataurl?: string;
		content?: string;
		upload_id?: string;
	},
): string | null {
	switch (sourceType) {
		case "url":
			if (!values.url) return i18n.t("skillsRepo.validation.urlRequired", T);
			if (!values.url.startsWith("http://") && !values.url.startsWith("https://")) {
				return i18n.t("skillsRepo.validation.urlScheme", T);
			}
			return null;
		case "dataurl":
			if (!values.dataurl) return i18n.t("skillsRepo.validation.dataUrlRequired", T);
			if (!values.dataurl.startsWith("data:") || !values.dataurl.includes(";base64,")) {
				return i18n.t("skillsRepo.validation.dataUrlFormat", T);
			}
			{
				const decodedLength = dataURLDecodedLength(values.dataurl);
				if (decodedLength != null) {
					const sizeErr = validateSkillFileSize(decodedLength);
					if (sizeErr) return sizeErr;
				}
			}
			return null;
		case "text":
			if (!values.content || !values.content.trim()) return i18n.t("skillsRepo.validation.textRequired", T);
			return validateSkillFileSize(utf8ByteLength(values.content));
		case "upload":
			return null; // Upload validation happens during the upload process
		default:
			return i18n.t("skillsRepo.validation.unknownSource", { ...T, type: sourceType });
	}
}

/** Validate file basename (no separators, traversal, or reserved SKILL.md) */
export function validateFilename(filename: string): string | null {
	const value = filename.trim();
	if (!value) return i18n.t("skillsRepo.validation.filenameRequired", T);
	if (value === "." || value === "..") return i18n.t("skillsRepo.validation.filenameDots", T);
	if (value.includes("/") || value.includes("\\")) return i18n.t("skillsRepo.validation.filenameFolders", T);
	if (value.toLowerCase() === "skill.md") return i18n.t("skillsRepo.validation.skillMdManaged", T);
	return null;
}

/** Validate relative file path (no traversal, absolute paths, or reserved SKILL.md) */
export function validateFilePath(path: string | undefined): string | null {
	if (!path || !path.trim()) return i18n.t("skillsRepo.validation.pathRequired", T);
	const value = path.trim();
	if (value.startsWith("/")) return i18n.t("skillsRepo.validation.pathRelative", T);
	if (value.endsWith("/")) return i18n.t("skillsRepo.validation.pathNotFolder", T);
	if (value.includes("\\")) return i18n.t("skillsRepo.validation.pathSlashes", T);
	const segments = value.split("/");
	if (segments.some((segment) => segment === "" || segment === "." || segment === "..")) {
		return i18n.t("skillsRepo.validation.pathSegments", T);
	}
	if (segments.length === 1 && segments[0].toLowerCase() === "skill.md") {
		return i18n.t("skillsRepo.validation.skillMdManaged", T);
	}
	return null;
}

/** Run all validations and return errors */
export function validateSkillForm(data: {
	name: string;
	description: string;
	version: string;
	skill_md_body: string;
	extra_frontmatter_json?: string;
	metadata_json?: string;
}): ValidationError[] {
	const errors: ValidationError[] = [];

	const nameErr = validateSkillName(data.name);
	if (nameErr) errors.push({ field: "name", message: nameErr });

	const descErr = validateDescription(data.description);
	if (descErr) errors.push({ field: "description", message: descErr });

	const versionErr = validateVersion(data.version);
	if (versionErr) errors.push({ field: "version", message: versionErr });

	const bodyResult = validateSkillMdBody(data.skill_md_body);
	if (bodyResult.error) errors.push({ field: "skill_md_body", message: bodyResult.error });

	if (data.extra_frontmatter_json) {
		const efErr = validateExtraFrontmatter(data.extra_frontmatter_json);
		if (efErr) errors.push({ field: "extra_frontmatter", message: efErr });
	}

	if (data.metadata_json) {
		const mdErr = validateMetadata(data.metadata_json);
		if (mdErr) errors.push({ field: "metadata", message: mdErr });
	}

	return errors;
}