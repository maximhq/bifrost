"""report-writer: turns SupportMetrics into a narrative and a PDF.

It opens Bifrost's MCP server too: before drafting, its LLM may look up one or
two facts the metrics lack (read-only). It returns the PDF as an A2A raw file
part, so the bytes never pass through an LLM.

    look up (optional: LLM 3/4/8 <-> MCP 5/6/7) -> draft (LLM) -> render PDF (local)
"""

from __future__ import annotations

import json

from a2a.types import a2a_pb2 as pb
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage

from revops_copilot.agents.reportwriter.pdf import render
from revops_copilot.common.a2aserver import FileBlob, TaskInput, TaskOutput, serve
from revops_copilot.common.config import AgentName
from revops_copilot.common.contracts import ReportDraft, SupportMetrics
from revops_copilot.common.toolloop import bifrost_tools, build_tool_loop, structured

AGENT = AgentName.REPORT_WRITER

SYSTEM = """You write concise customer-support health reports for company leadership, for the period given.
Use only the metrics given. Do not invent numbers or customer names. Plain business English.
3 to 5 sections. Each risk and recommendation is one sentence."""

LOOKUP = """You are about to write a customer-support report from the metrics below.
If one or two quick read-only lookups with the tools would add an important fact the metrics lack,
make them. Otherwise call no tools. Then reply with up to three short bullet points of extra facts,
or the single word: none. Never create, upload, update or delete anything."""


def _metrics_of(task: TaskInput) -> SupportMetrics | None:
    for d in task.data:
        if "support_metrics" in d:
            return SupportMetrics.model_validate(d["support_metrics"])
    return None


async def run(task: TaskInput) -> TaskOutput:
    metrics = _metrics_of(task)
    if metrics is None:
        return TaskOutput(text="Send a data part with support_metrics.")

    session = task.session
    llm = await session.chat_model(temperature=0.2)
    facts = json.dumps(metrics.model_dump(mode="json"), indent=2)
    extra = ""
    async with bifrost_tools(session, required=False) as (mcp, tools):
        if mcp is not None and tools:
            loop = build_tool_loop(llm, mcp, tools, LOOKUP, max_steps=3, reporter=session)
            state = await loop.ainvoke({"messages": [HumanMessage(f"{task.text}\n\nMetrics:\n{facts}")]})
            last = next((m for m in reversed(state["messages"]) if isinstance(m, AIMessage)), None)
            extra = "" if last is None or str(last.content).strip().lower() == "none" else str(last.content)
    async with session.step("llm", "draft the narrative", surfaces="③④⑧") as note:
        draft: ReportDraft = await structured(
            llm,
            ReportDraft,
            [
                SystemMessage(SYSTEM),
                HumanMessage(
                    f"{task.text}\n\nMetrics:\n{facts}" + (f"\n\nExtra facts from lookups:\n{extra}" if extra else "")
                ),
            ],
        )
        note["detail"] = f"'{draft.title}', {len(draft.sections)} sections"
    async with session.step("step", "render PDF with charts") as note:
        pdf = render(draft, metrics)
        note["detail"] = f"{len(pdf) // 1024} KB"
    filename = f"customer-health-{metrics.quarter_label.replace(' ', '-').lower()}.pdf"
    return TaskOutput(
        text=f"Rendered '{draft.title}' ({len(pdf) // 1024} KB, {len(draft.sections)} sections).",
        files=[FileBlob(filename, pdf, "application/pdf")],
        data={"title": draft.title, "filename": filename},
    )


SKILLS = [
    pb.AgentSkill(
        id="quarterly-report-pdf",
        name="Quarterly report PDF",
        description="Writes the narrative for support metrics and renders a PDF with charts.",
        tags=["report", "pdf"],
        examples=["Write the Q3 2026 customer health report"],
    )
]


def main() -> None:
    serve(AGENT, "Writes quarterly report narratives and renders PDFs, LLM via Bifrost.", SKILLS, run)


if __name__ == "__main__":
    main()
