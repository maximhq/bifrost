// Runtime registry for what a downstream build adds to the Logs page.
//
// OSS registers nothing: the log sheet's Routing tab shows its attempt trail and routing logs, the
// Metadata grid every caller key, and the filter sidebar its own sections. A downstream build can
// register a panel for the top of the Routing tab, labelled filter groups over metadata keys it
// writes, and the prefixes of those keys, by importing its registration module via the @enterprise
// alias; see ui/app/_fallbacks/enterprise/lib/registrations/logs.ts for the OSS-build fallback.

import type { LogEntry } from "@/lib/types/logs";
import type { ComponentType } from "react";

export interface LogRoutingPanelProps {
	log: LogEntry;
}

let logRoutingPanel: ComponentType<LogRoutingPanelProps> | undefined;

/**
 * Registers (or replaces) the panel drawn at the top of the log sheet's Routing tab. The panel
 * decides for itself whether a log has anything to show, and renders nothing when it has not.
 * Intended to be called at module load, once, before the first render that reads the registry.
 */
export function registerLogRoutingPanel(component: ComponentType<LogRoutingPanelProps>): void {
	logRoutingPanel = component;
}

/** Returns the registered Routing tab panel, or undefined in builds without one. */
export function getLogRoutingPanel(): ComponentType<LogRoutingPanelProps> | undefined {
	return logRoutingPanel;
}

// A labelled metadata filter offers the values of one metadata key under readable names. It writes
// the same metadata_filters entry as the generic Metadata section, one value per key, so a link that
// sets that entry lands on it checked.
export interface LabelledMetadataFilterField {
	key: string;
	label: string;
	options: { value: string; label: string }[];
}

// RecordedMetadataValues is, per metadata key, the values rows in a time range hold; undefined while
// they load or when they cannot be read.
export interface RecordedMetadataValues {
	values?: Record<string, string[]>;
	isLoading: boolean;
}

export interface LabelledMetadataFilterGroup {
	title: string;
	fields: LabelledMetadataFilterField[];
	// useRecordedValues, when given, reads which values rows in the Logs page's time range hold, so
	// the group lists only options it can match. It is a hook, called on every render of the group;
	// skip is true while the group is closed and nothing in it is checked.
	useRecordedValues?: (range: { startTime?: string; endTime?: string }, options: { skip: boolean }) => RecordedMetadataValues;
}

let metadataFilterGroups: LabelledMetadataFilterGroup[] = [];

/** Registers a filter group, replacing one with the same title. Same load-once contract as registerLogRoutingPanel. */
export function registerMetadataFilterGroup(group: LabelledMetadataFilterGroup): void {
	metadataFilterGroups = [...metadataFilterGroups.filter((g) => g.title !== group.title), group];
}

/**
 * listedOptions is what a field lists: every option while the recorded values are unknown, else the
 * options rows hold, in the field's order, then any recorded value the field does not name under
 * the value itself. The checked value stays listed either way, under the value itself when the
 * field does not name it, so it can be unchecked.
 */
export function listedOptions(
	field: LabelledMetadataFilterField,
	recorded: Record<string, string[]> | undefined,
	checked: string | undefined,
): LabelledMetadataFilterField["options"] {
	let options = field.options;
	if (recorded) {
		const held = new Set(recorded[field.key] ?? []);
		const named = field.options.filter((option) => held.has(option.value) || option.value === checked);
		const unnamed = [...held].filter((value) => !field.options.some((option) => option.value === value));
		options = [...named, ...unnamed.map((value) => ({ value, label: value }))];
	}
	if (!checked || options.some((option) => option.value === checked)) return options;
	return [...options, { value: checked, label: checked }];
}

/** Returns the registered filter groups, empty in builds without any. */
export function getMetadataFilterGroups(): readonly LabelledMetadataFilterGroup[] {
	return metadataFilterGroups;
}

// Reserved metadata prefixes mark keys a downstream build writes and shows in its own way, such as
// in its Routing tab panel, so the log sheet's Metadata grid leaves them out.
let reservedMetadataPrefixes: string[] = [];

/** Reserves a metadata key prefix. Same load-once contract as registerLogRoutingPanel. */
export function registerReservedMetadataPrefix(prefix: string): void {
	if (prefix && !reservedMetadataPrefixes.includes(prefix)) reservedMetadataPrefixes = [...reservedMetadataPrefixes, prefix];
}

/** Reports whether a metadata key falls under a reserved prefix. */
export function isReservedMetadataKey(key: string): boolean {
	return reservedMetadataPrefixes.some((prefix) => key.startsWith(prefix));
}