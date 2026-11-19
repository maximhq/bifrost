import { describe, expect, it } from "vitest";
import type { RelayFormValues } from "@/lib/types/schemas";
import { applyAsyncJob, globMatch, pairStatusRoutes, relayHeadersForPlan, statusCheckTarget, statusRouteFor } from "./relayAsyncJob";

type RouteForm = RelayFormValues["routes"][number];
type JobForm = NonNullable<RouteForm["job"]>;

const textractJob: JobForm = {
	id_path: "JobId",
	poll: {
		method: "POST",
		path: "/",
		headers: { "X-Amz-Target": "Textract.GetDocumentAnalysis" },
		body: '{"JobId":"{{job.id}}"}',
		content_type: "application/x-amz-json-1.1",
	},
	status_path: "JobStatus",
	succeeded: ["SUCCEEDED"],
};
const textractStart: RouteForm & { job: JobForm } = {
	method: "POST",
	path: "/",
	headers: [{ name: "X-Amz-Target", value: "Textract.StartDocumentAnalysis" }],
	job: textractJob,
};
const restJob: JobForm = { id_path: "id", poll: { method: "GET", path: "/jobs/{{job.id}}" }, status_path: "status", succeeded: ["done"] };
const restStart: RouteForm & { job: JobForm } = { method: "POST", path: "/jobs", headers: [], job: restJob };

describe("globMatch", () => {
	it("follows path.Match", () => {
		expect(globMatch("Textract.Get*", "Textract.GetDocumentAnalysis")).toBe(true);
		expect(globMatch("*", "a/b")).toBe(false);
		expect(globMatch("v[12]", "v2")).toBe(true);
		expect(globMatch("v[^12]", "v2")).toBe(false);
		expect(globMatch("a?c", "abc")).toBe(true);
		expect(globMatch("a\\*", "a*")).toBe(true);
		expect(globMatch("a\\*", "ab")).toBe(false);
		expect(globMatch("[a", "a")).toBe(false);
	});
});

describe("statusRouteFor", () => {
	it("matches the poll's headers on its path", () => {
		expect(statusRouteFor(textractJob)).toEqual({
			method: "POST",
			path: "/",
			headers: [{ name: "X-Amz-Target", value: "Textract.GetDocumentAnalysis" }],
			enabled: true,
			billable: false,
		});
	});

	it("ends in /** where the id starts, since an id may hold a slash", () => {
		expect(statusRouteFor(restJob)).toMatchObject({ method: "GET", path: "/jobs/**", headers: [] });
		expect(statusRouteFor({ ...restJob, poll: { method: "GET", path: "/v1/jobs/{{job.id}}/status?full=1" } }).path).toBe("/v1/jobs/**");
		// A GET route is free by default, so it carries no billable flag.
		expect(statusRouteFor(restJob).billable).toBeUndefined();
	});
});

describe("applyAsyncJob", () => {
	it("adds a free status route when none takes the status check", () => {
		const { routes, plan } = applyAsyncJob([], -1, textractStart);
		expect(plan.kind).toBe("add");
		expect(routes).toHaveLength(2);
		expect(routes[1]).toMatchObject({ method: "POST", path: "/", billable: false });
		expect(statusCheckTarget(routes, textractJob).kind).toBe("free");
	});

	it("reuses a free route that already takes the status check", () => {
		const existing: RouteForm = { method: "POST", path: "/", headers: [{ name: "X-Amz-Target", value: "Textract.Get*" }], billable: false };
		const { routes, plan } = applyAsyncJob([existing], -1, textractStart);
		expect(plan).toEqual({ kind: "reuse", index: 0 });
		expect(routes).toHaveLength(2);
	});

	it("never makes a billed route free; a narrower free route goes ahead of a billed catch-all", () => {
		const catchAll: RouteForm = { method: "GET", path: "/jobs/**", headers: [], billable: true };
		const { routes, plan } = applyAsyncJob([catchAll], -1, restStart);
		// The new free route is the same glob, so the billed one would still win: refuse rather than flip it.
		expect(plan.kind).toBe("blocked");
		expect(routes[0]).toEqual(catchAll);
	});

	it("adds a free route ahead of a broader billed one", () => {
		const anyGet: RouteForm = { method: "GET", path: "/**", headers: [], billable: true };
		const { routes, plan } = applyAsyncJob([anyGet], -1, restStart);
		expect(plan.kind).toBe("add");
		expect(statusCheckTarget(routes, restJob).kind).toBe("free");
	});

	it("matches a route on the relay's extra header, unless the poll sets its own", () => {
		const routes: RouteForm[] = [
			{ method: "GET", path: "/jobs/**", headers: [{ name: "X-Mode", value: "check" }], billable: true },
			{ method: "GET", path: "/jobs/**", headers: [], billable: false },
		];
		const relay = relayHeadersForPlan({ "X-Mode": { value: "check" } });
		expect(statusCheckTarget(routes, restJob, relay)).toEqual({ kind: "billed", index: 0 });
		const own = { ...restJob, poll: { ...restJob.poll, headers: { "x-mode": "other" } } };
		expect(statusCheckTarget(routes, own, relay)).toEqual({ kind: "free", index: 1 });
	});

	it("never reports a free route when a secret relay header decides the match", () => {
		const routes: RouteForm[] = [
			{ method: "GET", path: "/jobs/**", headers: [{ name: "X-Mode", value: "check" }], billable: true },
			{ method: "GET", path: "/jobs/**", headers: [], billable: false },
		];
		const relay = relayHeadersForPlan({ "X-Mode": { value: "", ref: "env.MODE", type: "env" } });
		expect(statusCheckTarget(routes, restJob, relay)).toEqual({ kind: "unverified" });
		expect(applyAsyncJob(routes, -1, restStart, relay).plan).toEqual({ kind: "unverified" });
	});

	it("refuses a save that moves another job's status checks to a charged route", () => {
		const other: JobForm = { ...restJob, poll: { method: "POST", path: "/", headers: { "X-Operation": "check", "X-Tenant": "one" } } };
		const existing: RouteForm[] = [
			{ method: "POST", path: "/start", headers: [], job: other },
			{ method: "POST", path: "/", headers: [{ name: "X-Operation", value: "check" }], billable: false },
		];
		const charged = {
			...restStart,
			path: "/",
			headers: [
				{ name: "X-Operation", value: "check" },
				{ name: "X-Tenant", value: "one" },
			],
		};
		expect(applyAsyncJob(existing, -1, charged).plan).toMatchObject({
			kind: "otherJobCharged",
			job: { path: "/start" },
			route: { path: "/" },
		});
		expect(applyAsyncJob(existing, -1, restStart).plan.kind).toBe("add");
	});

	it("refuses a start route that an identical route would shadow", () => {
		const existing: RouteForm = { method: "POST", path: "/jobs", headers: [], billable: true };
		expect(applyAsyncJob([existing], -1, restStart).plan).toEqual({ kind: "duplicateStart", index: 0 });
		// Converting that route in place, or a start with its own header, is fine.
		expect(applyAsyncJob([existing], 0, restStart).plan.kind).toBe("add");
		const distinct = { ...restStart, headers: [{ name: "X-Kind", value: "a" }] };
		expect(applyAsyncJob([existing], -1, distinct).plan.kind).toBe("add");
	});

	it("escapes literal header text in a generated status route", () => {
		const job = { ...restJob, poll: { ...restJob.poll, headers: { "X-Mode": "[draft]", "X-Job": "id-{{job.id}}" } } };
		const route = statusRouteFor(job);
		expect(route.headers).toEqual([
			{ name: "X-Mode", value: "\\[draft]" },
			{ name: "X-Job", value: "id-*" },
		]);
		expect(applyAsyncJob([{ method: "GET", path: "/**", headers: [], billable: true }], -1, { ...restStart, job }).plan.kind).toBe("add");
	});

	it("is blocked when only some job ids reach a free route", () => {
		// A free /jobs/* misses the id with a slash, which falls through to the billed catch-all.
		const routes: RouteForm[] = [
			{ method: "GET", path: "/jobs/*", headers: [], billable: false },
			{ method: "GET", path: "/**", headers: [], billable: true },
		];
		expect(statusCheckTarget([...routes, restStart], restJob)).toEqual({ kind: "billed", index: 1 });

		// The generated route cannot pass an exact charged route, so the save itself is refused.
		const exact: RouteForm = { method: "GET", path: "/jobs/jobs/1", headers: [], billable: true };
		expect(applyAsyncJob([exact], -1, restStart).plan).toMatchObject({ kind: "blocked", route: { path: "/jobs/jobs/1" } });
	});

	it("does not reuse a disabled route, which answers status checks with 404", () => {
		const disabled: RouteForm = { method: "GET", path: "/jobs/**", headers: [], billable: false, enabled: false };
		const { routes, plan } = applyAsyncJob([disabled], -1, restStart);
		expect(statusCheckTarget([disabled, restStart], restJob)).toEqual({ kind: "disabled", index: 0 });
		expect(plan).toEqual({ kind: "disabled", route: disabled });
		expect(routes).toHaveLength(2);
	});

	it("adds a free route ahead of a broader disabled one", () => {
		const disabled: RouteForm = { method: "GET", path: "/**", headers: [], billable: false, enabled: false };
		const { routes, plan } = applyAsyncJob([disabled], -1, restStart);
		expect(plan.kind).toBe("add");
		expect(statusCheckTarget(routes, restJob).kind).toBe("free");
	});

	it("replaces the edited start route in place", () => {
		const { routes } = applyAsyncJob([restStart, statusRouteFor(restJob)], 0, { ...restStart, path: "/v2/jobs" });
		expect(routes[0].path).toBe("/v2/jobs");
		expect(routes).toHaveLength(2);
	});
});

describe("pairStatusRoutes", () => {
	it("maps each status route to its job route", () => {
		const routes: RouteForm[] = [restStart, { method: "POST", path: "/search", headers: [] }, statusRouteFor(restJob)];
		expect([...pairStatusRoutes(routes)]).toEqual([[2, 0]]);
	});

	it("leaves a billed route that takes the status check unpaired", () => {
		const routes: RouteForm[] = [restStart, { method: "GET", path: "/jobs/**", headers: [], billable: true }];
		expect(pairStatusRoutes(routes).size).toBe(0);
	});
});