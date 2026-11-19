import { describe, expect, it } from "vitest";
import type { RelayFormValues } from "@/lib/types/schemas";
import { applyAsyncJob, jobBillsRequestUnits } from "./relayAsyncJob";

type RouteForm = RelayFormValues["routes"][number];
type JobForm = NonNullable<RouteForm["job"]>;

const restJob: JobForm = { id_path: "id", poll: { method: "GET", path: "/jobs/{{job.id}}" }, status_path: "status", succeeded: ["done"] };
const restStart: RouteForm & { job: JobForm } = { method: "POST", path: "/jobs", headers: [], job: restJob };

describe("applyAsyncJob", () => {
	it("appends a new start route and adds no status route, since the gateway relays status checks free", () => {
		const other: RouteForm = { method: "GET", path: "/health", headers: [] };
		expect(applyAsyncJob([other], -1, restStart)).toEqual({ routes: [other, restStart] });
	});

	it("replaces the edited start route in place", () => {
		const edited = { ...restStart, path: "/v2/jobs" };
		expect(applyAsyncJob([restStart], 0, edited)).toEqual({ routes: [edited] });
	});

	it("refuses a start route that an identical route would shadow", () => {
		const existing: RouteForm = { method: "POST", path: "/jobs", headers: [], billable: true };
		expect(applyAsyncJob([existing], -1, restStart).duplicateOf).toBe(0);
		// Blank header matchers are dropped on save, so they do not tell two routes apart.
		const blank = { ...restStart, headers: [{ name: "X-Op", value: " " }] };
		expect(applyAsyncJob([existing], -1, blank).duplicateOf).toBe(0);
		expect(applyAsyncJob([restStart], 0, restStart).duplicateOf).toBeUndefined();
	});
});

describe("jobBillsRequestUnits", () => {
	it("reads the job's pricing merged over the route's and the relay's", () => {
		expect(jobBillsRequestUnits({ units_source: "request", cost_per_unit: 0.01 }, undefined, undefined)).toBe(true);
		expect(jobBillsRequestUnits({ units_source: "request", cost_per_unit: 0.01 }, undefined, { units_source: "response" })).toBe(false);
		expect(jobBillsRequestUnits({ units_source: "request", cost_per_unit: 0.01 }, { cost_per_unit: 0 }, null)).toBe(false);
		expect(jobBillsRequestUnits({ units_source: "request" }, undefined, { units_source: "", cost_per_unit: null })).toBe(false);
	});
});