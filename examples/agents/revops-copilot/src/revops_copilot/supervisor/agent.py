"""revops-copilot: the supervisor, a tool-using agent.

It holds the example's one Bifrost identity and answers whatever it is asked.
It has two kinds of tools:

* every MCP tool Bifrost's MCP server (/mcp) grants the identity, for quick
  lookups (surfaces 5, 6, 7);
* A2A calls through the Agent Gateway (surfaces 1 and 2), which forward the
  identity to the subagent and reuse this task's context id, so the whole job
  is one Bifrost session in Agent Logs:

    ask_support_analyst(question)       any question about support data in Pylon
    prepare_report(start, end, ...)     when a report, PDF or upload is asked for:
        support-analyst (metrics) -> report-writer (PDF) -> publisher (Drive, optional)

    LLM decides (surfaces 3/4/8) -> tool (A2A) -> LLM ... -> answer
    Any refusal stops the job and reports the surface that fired.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date
from typing import Any

import openai
from a2a.types import a2a_pb2 as pb
from langchain_core.messages import AIMessage, BaseMessage, HumanMessage, SystemMessage, ToolMessage
from pydantic import BaseModel, Field, ValidationError, field_validator

from revops_copilot.common.a2aserver import AgentReply, FileBlob, TaskInput, TaskOutput, call_agent, serve
from revops_copilot.common.bifrost import BifrostSession, llm_refusal
from revops_copilot.common.config import AgentName
from revops_copilot.common.contracts import ReportRequest
from revops_copilot.common.policy import PolicyDenied
from revops_copilot.common.toolloop import bifrost_tools, call_mcp_tool

AGENT = AgentName.SUPERVISOR
MAX_ROUNDS = 6


class ask_support_analyst(BaseModel):  # noqa: N801 - the class name is the tool name the model sees
    """Ask the support analyst a question about support data in Pylon: issues,
    accounts, SLAs, response times, themes. It answers in plain text."""

    question: str = Field(description="The question, with explicit dates (e.g. 'between 2026-11-03 and 2026-11-09')")


class prepare_report(BaseModel):  # noqa: N801
    """Build a customer-support health report as a PDF for any period, and upload it to
    Google Drive. Use when the user asks for a report, a PDF, or to upload/push one to Drive."""

    start_date: date = Field(description="First day of the period, YYYY-MM-DD")
    end_date: date = Field(description="Last day of the period, inclusive, YYYY-MM-DD")
    label: str = Field(default="", description="Short name for the period, e.g. 'Q3 2026', 'Yesterday (2026-11-10)'")
    audience: str = "leadership"
    focus: str = Field(default="", description="Anything the user asked to emphasise")
    upload: bool = Field(default=True, description="Upload the PDF to Google Drive. False when the user says not to.")

    @field_validator("start_date", "end_date", mode="before")
    @classmethod
    def _day(cls, value: Any) -> Any:
        """Models sometimes send timestamps; a report covers whole days, so keep the date part."""
        if isinstance(value, str) and len(value) > 10 and value[4] == "-" and value[10] in "T ":
            return value[:10]
        return value


def _system_prompt() -> str:
    return f"""You are revops-copilot, a RevOps assistant for the support team. Today is {date.today().isoformat()}.

Answer the user's question. Use the tools when you need data:
- The MCP tools (named <server>-<tool>, for example Pylon-search_issues) for a quick, single lookup.
- ask_support_analyst: questions that need several lookups or analysis of support issues, accounts,
  SLAs or tickets. Turn relative dates ("last week", "Q3") into explicit dates in the question.
- prepare_report: when the user asks for a report or PDF, or to upload or push one to Drive.
  Turn the period into dates: "yesterday" is a single day, "last week" is the last seven days,
  "Q3 2026" is 2026-07-01 to 2026-09-30. Do not ask for confirmation; the request is the confirmation.
Never create, upload or change anything with an MCP tool; reports go through prepare_report.

Answer directly when no data is needed. Keep answers short and lead with the number or fact asked for."""


@dataclass
class Job:
    """What the tools produced, for the final TaskOutput."""

    pdf: FileBlob | None = None
    publish: dict[str, Any] = field(default_factory=dict)
    steps: list[dict[str, str]] = field(default_factory=list)


class DelegationFailed(Exception):
    """A subagent's task ended without completing, so its output cannot be used as an answer."""

    def __init__(self, target: AgentName, state: str, text: str) -> None:
        super().__init__(f"{target.value} did not finish ({state}): {text or 'no details'}")


async def _delegate(session: BifrostSession, job: Job, target: AgentName, text: str, **kw: Any) -> AgentReply:
    reply = await call_agent(session, target, text, **kw)
    job.steps.append({"agent": target.value, "state": reply.state, "summary": reply.text[:300]})
    if reply.denied is not None:
        raise reply.denied
    if reply.state != "TASK_STATE_COMPLETED":
        raise DelegationFailed(target, reply.state, reply.text[:500])
    return reply


async def _ask(session: BifrostSession, job: Job, call: ask_support_analyst) -> str:
    reply = await _delegate(session, job, AgentName.SUPPORT_ANALYST, call.question)
    return reply.text or "(no answer)"


async def _report(session: BifrostSession, job: Job, call: prepare_report) -> str:
    req = ReportRequest(start=call.start_date, end=call.end_date, label=call.label, audience=call.audience, focus=call.focus)
    analysis = await _delegate(
        session, job, AgentName.SUPPORT_ANALYST, f"Support health for {req.label}.", data=req.model_dump(mode="json")
    )
    written = await _delegate(
        session,
        job,
        AgentName.REPORT_WRITER,
        f"Write the {req.label} customer health report for {req.audience}. {req.focus}",
        data={"support_metrics": analysis.data.get("support_metrics", {})},
    )
    job.pdf = next((f for f in written.files if f.media_type == "application/pdf"), None)
    if job.pdf is None:
        return f"report-writer returned no PDF: {written.text}"
    if not call.upload:
        return f"PDF ready ({len(job.pdf.data) // 1024} KB, {job.pdf.name}); not uploaded, as asked."
    published = await _delegate(
        session, job, AgentName.PUBLISHER, f"{req.label} customer health report for {req.audience}.", files=[job.pdf]
    )
    job.publish = published.data.get("publish", {})
    return f"Uploaded {job.pdf.name}: {job.publish.get('link') or published.text}"


TOOLS: dict[str, tuple[type[BaseModel], Any]] = {
    "ask_support_analyst": (ask_support_analyst, _ask),
    "prepare_report": (prepare_report, _report),
}


async def run(task: TaskInput) -> TaskOutput:
    session = task.session
    job = Job()
    try:
        async with bifrost_tools(session, required=False) as (mcp, mcp_tools):
            return await _loop(session, job, task.text, mcp, mcp_tools)
    except PolicyDenied as denied:
        # A hop (the supervisor's LLM or MCP call, an A2A call, or a subagent's MCP/LLM) was refused.
        return TaskOutput(
            text=f"Stopped by Bifrost policy at surface {denied.surface} on the {denied.hop} hop "
            f"({denied.status} {denied.code}).\n{denied.message}",
            data={"steps": job.steps},
            denied=denied,
        )
    except DelegationFailed as failure:
        return TaskOutput(text=str(failure), data={"steps": job.steps}, failed=True)


async def _loop(session: BifrostSession, job: Job, ask: str, mcp: Any, mcp_tools: list[Any]) -> TaskOutput:
    base = await session.chat_model()
    llm = base.bind_tools([*(schema for schema, _ in TOOLS.values()), *mcp_tools])
    mcp_names = {t.name for t in mcp_tools}
    messages: list[BaseMessage] = [SystemMessage(_system_prompt()), HumanMessage(ask)]
    for round_ in range(1, MAX_ROUNDS + 1):
        last = round_ == MAX_ROUNDS
        if last:
            # No tools on the last round: answer from what the tools already returned.
            messages.append(HumanMessage("Answer the question now, using only the information gathered above."))
        title = "write the answer" if last else f"decide next step ({round_}/{MAX_ROUNDS})"
        async with session.step("llm", title, surfaces="③④⑧") as note:
            try:
                reply: AIMessage = await (base if last else llm).ainvoke(messages)
            except openai.APIStatusError as e:
                raise llm_refusal(e) from e
            calls = getattr(reply, "tool_calls", None) or []
            note["detail"] = ", ".join(c["name"] for c in calls) if calls else "answer ready"
        messages.append(reply)
        if not calls:
            return _output(str(reply.content), job)
        for c in calls:
            if c["name"] in mcp_names:
                text, error = await call_mcp_tool(mcp, c["name"], c["args"], session)
                messages.append(ToolMessage(text, tool_call_id=c["id"], status="error" if error else "success"))
                continue
            schema, fn = TOOLS.get(c["name"], (None, None))
            if schema is None:
                messages.append(ToolMessage(f"unknown tool {c['name']}", tool_call_id=c["id"], status="error"))
                continue
            try:
                call = schema.model_validate(c["args"])
            except ValidationError as e:
                # Bad arguments are the model's mistake: let it correct them, don't crash the task.
                detail = "; ".join(f"{'.'.join(map(str, err['loc']))}: {err['msg']}" for err in e.errors())
                await session.progress("step", f"{c['name']} arguments rejected", detail=detail[:160], status="fail")
                messages.append(ToolMessage(f"invalid arguments: {detail}", tool_call_id=c["id"], status="error"))
                continue
            result = await fn(session, job, call)
            messages.append(ToolMessage(result, tool_call_id=c["id"]))
    return _output("No answer.", job)  # unreachable: the last round has no tools


def _output(answer: str, job: Job) -> TaskOutput:
    return TaskOutput(
        text=answer,
        files=[job.pdf] if job.pdf is not None else [],
        data={"steps": job.steps, "publish": job.publish},
    )


SKILLS = [
    pb.AgentSkill(
        id="revops-questions",
        name="RevOps questions",
        description="Answers questions about support issues, accounts and SLAs from Pylon.",
        tags=["revops", "pylon", "support"],
        examples=["How many issues were raised last week?", "Which accounts breached SLA most in Q3?"],
    ),
    pb.AgentSkill(
        id="customer-health-report",
        name="Customer health report",
        description="Builds a customer health report PDF for any period and uploads it to Google Drive.",
        tags=["revops", "report", "pdf", "google-drive"],
        examples=["Prepare the Q3 2026 customer health report and upload the PDF to the shared drive"],
    ),
]


def main() -> None:
    serve(
        AGENT,
        "RevOps copilot: answers support questions and builds reports through the Bifrost Agent Gateway.",
        SKILLS,
        run,
    )


if __name__ == "__main__":
    main()
