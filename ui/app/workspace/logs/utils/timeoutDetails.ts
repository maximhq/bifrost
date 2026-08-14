import type { BifrostError } from "@/lib/types/logs";

const timeoutReason: Record<NonNullable<BifrostError["extra_fields"]>["timeout_source"] & string, string> = {
	bifrost_context_deadline: "Bifrost context deadline was reached",
	bifrost_http_client_timeout: "Bifrost HTTP client reached the configured provider timeout",
	upstream_connection_timeout: "Upstream connection or proxy timed out before returning a response",
	upstream_connection_error: "Upstream connection or proxy disconnected before returning a response",
	upstream_http_504: "Upstream returned HTTP 504 Gateway Timeout",
	unknown_timeout: "Timeout source could not be determined; it was not attributed to the configured provider timeout",
};

export interface TimeoutDetail {
	label: string;
	value: string;
}

export function getTimeoutDetails(error?: BifrostError): TimeoutDetail[] {
	const fields = error?.extra_fields;
	if (!fields?.timeout_source) return [];

	const details: TimeoutDetail[] = [
		{ label: "Reason", value: timeoutReason[fields.timeout_source] },
		{ label: "Source", value: fields.timeout_source },
	];
	if (typeof fields.elapsed_ms === "number") {
		details.push({ label: "Elapsed", value: `${(fields.elapsed_ms / 1000).toFixed(2)} s (${fields.elapsed_ms} ms)` });
	}
	if (typeof fields.configured_timeout_seconds === "number" && fields.configured_timeout_seconds > 0) {
		details.push({ label: "Configured timeout", value: `${fields.configured_timeout_seconds} s` });
	}
	if (typeof fields.upstream_response_received === "boolean") {
		details.push({ label: "Upstream response received", value: fields.upstream_response_received ? "Yes" : "No" });
	}
	return details;
}
