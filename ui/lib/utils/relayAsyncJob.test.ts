import { describe, expect, it } from "vitest";
import type { RelayRouteForm as RouteForm } from "@/lib/types/schemas";
import {
	applyAsyncJob,
	pairStatusRoutes,
	relayHeadersForPlan,
	statusCheckTarget,
	statusRouteFor,
	unusedStatusRoutes,
} from "./relayAsyncJob";
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

describe("statusRouteFor", () => {
	it("escapes glob characters in the fixed text, so the route matches its own job's checks", () => {
		expect(statusRouteFor({ ...restJob, poll: { method: "GET", path: "/v1/jobs[x]/{{job.id}}" } }).path).toBe("/v1/jobs\\[x\\]/**");
		expect(statusRouteFor({ ...restJob, poll: { method: "GET", path: "/v1/a*b/status", body: "{{job.id}}" } }).path).toBe(
			"/v1/a\\*b/status",
		);
	});

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
		expect(statusRouteFor({ ...restJob, poll: { method: "GET", path: "/v1/jobs/{{job.id}}/status?full=1" } }).path).toBe(
			"/v1/jobs/**/status",
		);
		// A GET route is free by default, so it carries no billable flag.
		expect(statusRouteFor(restJob).billable).toBeUndefined();
	});
});

describe("applyAsyncJob", () => {
	it("tells apart start routes whose header values hold spaces and colons", () => {
		const existing: RouteForm = { method: "POST", path: "/", headers: [{ name: "X", value: "a y: b" }], billable: true };
		const start = {
			...restStart,
			path: "/",
			headers: [
				{ name: "X", value: "a" },
				{ name: "Y", value: "b" },
			],
		};
		expect(applyAsyncJob([existing], -1, start).plan.kind).not.toBe("duplicateStart");
	});

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

	it("keeps other calls charged when the status path has text after the id", () => {
		const job: JobForm = { ...restJob, poll: { method: "GET", path: "/jobs/{{job.id}}/status" } };
		const start = { ...restStart, job };
		const charged: RouteForm = { method: "GET", path: "/**", headers: [], billable: true };
		const { routes, plan } = applyAsyncJob([charged], -1, start);
		expect(plan).toMatchObject({ kind: "add", route: { path: "/jobs/**/status" } });
		expect(statusCheckTarget(routes, job).kind).toBe("free");
		const metrics: JobForm = { ...job, poll: { method: "GET", path: "/jobs/metrics" } };
		expect(statusCheckTarget(routes, metrics).kind).toBe("billed");
	});

	it("refuses a generated route that would pass a deeper charged catch-all", () => {
		const job: JobForm = { ...restJob, poll: { method: "GET", path: "/jobs/{{job.id}}/status" } };
		const start = { ...restStart, job };
		const charged: RouteForm = { method: "GET", path: "/**", headers: [], billable: true };
		const premium: RouteForm = { method: "GET", path: "/jobs/premium/**", headers: [], billable: true };
		expect(applyAsyncJob([charged, premium], -1, start).plan).toMatchObject({ kind: "blocked", route: { path: "/jobs/premium/**" } });
		expect(applyAsyncJob([charged, { ...premium, billable: false }], -1, start).plan).toMatchObject({
			kind: "add",
			route: { path: "/jobs/**/status" },
		});
	});

	it("pairs a status route in the order the gateway tries stored routes", () => {
		const job: JobForm = { ...restJob, poll: { method: "GET", path: "/a" } };
		const stored: RouteForm[] = [
			{ method: "POST", path: "/start", headers: [], job },
			{ method: "GET", path: "/[ac]", headers: [], enabled: false },
			{ method: "GET", path: "/[ab]", headers: [], billable: false },
		];
		expect(pairStatusRoutes(stored).size).toBe(0);
		expect(pairStatusRoutes([stored[0], stored[2], stored[1]]).get(1)).toBe(0);
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
			{ name: "X-Mode", value: "\\[draft\\]" },
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

describe("unused status routes", () => {
	const postJob: JobForm = { ...restJob, poll: { method: "POST", path: "/jobs/{{job.id}}" } };
	const shape = (routes: RouteForm[]) => routes.map((r) => `${r.method} ${r.path}`);

	it("replaces the generated route when an edit changes the poll method", () => {
		const first = applyAsyncJob([], -1, { ...restStart, job: postJob });
		expect(shape(first.routes)).toEqual(["POST /jobs", "POST /jobs/**"]);
		const edited = applyAsyncJob(first.routes, 0, restStart);
		expect(shape(edited.routes)).toEqual(["POST /jobs", "GET /jobs/**"]);
		expect(shape(edited.removed)).toEqual(["POST /jobs/**"]);
	});

	it("keeps the status route when an edit leaves the poll alone", () => {
		const first = applyAsyncJob([], -1, restStart);
		const edited = applyAsyncJob(first.routes, 0, { ...restStart, job: { ...restJob, interval_seconds: 60 } });
		expect(shape(edited.routes)).toEqual(["POST /jobs", "GET /jobs/**"]);
		expect(edited.removed).toEqual([]);
	});

	it("drops the generated route with the job's start route, but not one another job still uses", () => {
		const first = applyAsyncJob([], -1, restStart);
		expect(unusedStatusRoutes(first.routes, first.routes.slice(1))).toEqual([0]);
		const other: RouteForm & { job: JobForm } = { ...restStart, path: "/batch" };
		const both = applyAsyncJob(first.routes, -1, other).routes;
		expect(shape(both)).toEqual(["POST /jobs", "GET /jobs/**", "POST /batch"]);
		expect(
			unusedStatusRoutes(
				both,
				both.filter((_, i) => i !== 0),
			),
		).toEqual([]);
	});

	it("keeps a generated route a remaining job reaches for some ids, though another route takes the first", () => {
		// Job A polls /jobs/{{job.id}}: the sample id job-id reaches an exact route, the others the wildcard.
		const a = applyAsyncJob([], -1, restStart).routes;
		const exactJob: RouteForm & { job: JobForm } = {
			...restStart,
			path: "/exact",
			job: { ...restJob, poll: { method: "GET", path: "/jobs/job-id" } },
		};
		const both = [...a, exactJob, { method: "GET" as const, path: "/jobs/job-id", headers: [] }];
		const withoutExactJob = both.filter((r) => r !== exactJob);
		expect(unusedStatusRoutes(both, withoutExactJob)).toEqual([]);
	});

	it("keeps the generated route when a secret header decides where the deleted last job's checks land", () => {
		const first = applyAsyncJob([], -1, restStart);
		const secret = relayHeadersForPlan({ "X-Mode": { value: "", ref: "env.MODE", type: "env" } });
		const guarded: RouteForm = { method: "GET", path: "/jobs/**", headers: [{ name: "X-Mode", value: "check" }], billable: false };
		const routes = [first.routes[0], guarded, first.routes[1]];
		expect(unusedStatusRoutes(routes, routes.slice(1), secret)).toEqual([]);
		// With the header readable, cleanup still runs.
		expect(unusedStatusRoutes(routes, routes.slice(1))).toEqual([1]);
	});

	it("never drops a route that is not shaped like a generated one, or a billed one", () => {
		const first = applyAsyncJob([], -1, restStart);
		const routes: RouteForm[] = [
			first.routes[0],
			{ method: "GET", path: "/jobs/*", headers: [] },
			{ method: "GET", path: "/jobs/**", headers: [], billable: true },
		];
		expect(unusedStatusRoutes(routes, routes.slice(1))).toEqual([]);
	});
});