"""support-analyst: answers support questions from Pylon, read-only.

Two modes:
* question (plain text, no data part): answer it with the Pylon tools.
* report (a ReportRequest data part): gather the period's SupportMetrics.


It opens Bifrost's MCP server (/mcp) and uses whichever granted tools fit.
Pylon's OAuth tokens live in Bifrost; this process never sees them.

    discover (MCP list_tools, surface 5)
      -> tool loop (LLM 3/4/8 <-> MCP 6/7)
      -> structured SupportMetrics (LLM)
"""

from __future__ import annotations

from datetime import date

from a2a.types import a2a_pb2 as pb
from langchain_core.messages import AIMessage
from langchain_core.messages import HumanMessage, SystemMessage

from revops_copilot.common.a2aserver import TaskInput, TaskOutput, serve
from revops_copilot.common.config import AgentName
from revops_copilot.common.contracts import ReportRequest, SupportMetrics
from revops_copilot.common.toolloop import ToolLoopExhausted, bifrost_tools, build_tool_loop, structured

AGENT = AgentName.SUPPORT_ANALYST

SYSTEM = """You are a support operations analyst with read-only access to Pylon,
the customer support platform, through the tools provided.

Gather the facts for the requested period:
- issues created in the date range, how many were resolved
- issues by month and by priority
- SLA breaches and first-response times, if the tools expose them
- the accounts with the most open issues or breaches
- recurring themes across issue titles

Rules:
- Use only the tools. Never invent numbers; record anything you cannot get under data gaps.
- Page through results when a tool paginates. Prefer filters over pulling everything.
- Customer personal data (emails, phone numbers) is not needed for this report.
- Do not attempt to create, update or delete anything."""


def _question_prompt() -> str:
    return f"""You are a support operations analyst with read-only access to Pylon,
the customer support platform, through the tools provided. Today is {date.today().isoformat()}.

Answer the question you are given:
- Use only the tools. Never invent numbers; if a tool cannot answer, say what is missing.
- Prefer one filtered query over pulling everything. Stop as soon as you can answer.
- Lead with the number or fact asked for, then one or two lines of context.
- Customer personal data (emails, phone numbers) is not needed; leave it out.
- Do not attempt to create, update or delete anything."""


async def run(task: TaskInput) -> TaskOutput:
    report = next((d for d in task.data if "start" in d and "end" in d), None)
    if report is None:
        question = task.text.strip()
        if not question:
            return TaskOutput(text="Ask a question, or send a ReportRequest data part (start, end).")
        return await _answer(task, question)
    req = ReportRequest.model_validate(report)

    session = task.session
    async with bifrost_tools(session) as (mcp, tools):
        assert mcp is not None  # required=True: no MCP access raises instead
        llm = await session.chat_model()
        loop = build_tool_loop(llm, mcp, tools, SYSTEM, reporter=session)
        ask = (
            f"Period: {req.label} ({req.start.isoformat()} to {req.end.isoformat()} inclusive).\n"
            f"Audience: {req.audience}. {req.focus}\n{task.text}"
        )
        try:
            state = await loop.ainvoke({"messages": [HumanMessage(ask)]})
        except ToolLoopExhausted as exhausted:
            # A report from partial facts would look complete; fail the task instead.
            return TaskOutput(text=str(exhausted), failed=True)

    findings = state["messages"]
    async with session.step("llm", "structure findings into SupportMetrics", surfaces="③④⑧") as note:
        metrics: SupportMetrics = await structured(
            llm,
            SupportMetrics,
            [
                SystemMessage("Turn the analyst transcript into the SupportMetrics schema. Copy numbers exactly."),
                *findings,  # starts with the ask: period, audience and focus
                HumanMessage(f"Fill SupportMetrics for {req.label}. quarter_label must be '{req.label}'."),
            ],
        )
        note["detail"] = f"{metrics.total_issues} issues, {metrics.sla_breaches} SLA breaches"
    return TaskOutput(
        text=f"{req.label}: {metrics.total_issues} issues, {metrics.sla_breaches} SLA breaches "
        f"across {len(metrics.top_accounts)} highlighted accounts. Tools available: {len(tools)}.",
        data={"support_metrics": metrics.model_dump(mode="json")},
    )


async def _answer(task: TaskInput, question: str) -> TaskOutput:
    session = task.session
    async with bifrost_tools(session) as (mcp, tools):
        assert mcp is not None  # required=True: no MCP access raises instead
        llm = await session.chat_model()
        loop = build_tool_loop(llm, mcp, tools, _question_prompt(), max_steps=8, reporter=session)
        try:
            state = await loop.ainvoke({"messages": [HumanMessage(question)]})
        except ToolLoopExhausted as exhausted:
            return TaskOutput(text=str(exhausted), failed=True)
    last = next((m for m in reversed(state["messages"]) if isinstance(m, AIMessage)), None)
    return TaskOutput(text=str(last.content) if last is not None else "No answer.")


SKILLS = [
    pb.AgentSkill(
        id="support-health",
        name="Quarterly support health",
        description="Pulls issue volume, SLA breaches, top accounts and themes for a period from Pylon.",
        tags=["pylon", "support", "read-only"],
        examples=["Support health for Q3 2026"],
    )
]


def main() -> None:
    serve(AGENT, "Read-only support analyst over Pylon, via the Bifrost MCP Gateway.", SKILLS, run)


if __name__ == "__main__":
    main()
