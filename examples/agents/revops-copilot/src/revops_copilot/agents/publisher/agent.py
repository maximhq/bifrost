"""publisher: uploads the report PDF to a fixed Google Drive folder.

It opens Bifrost's MCP server (/mcp) and picks the upload tool from the tools
the identity is granted; Drive's delete and share tools are never granted. The upload is a deterministic tool call: the LLM never sees
or writes the PDF bytes, it only writes the file description.

    discover (MCP, surface 5)
      -> describe (LLM, surfaces 3/4/8)
      -> upload (MCP, surfaces 6/7: the DLP rule scans these arguments)
"""

from __future__ import annotations

import openai
from a2a.types import a2a_pb2 as pb
from langchain_core.messages import HumanMessage, SystemMessage

from revops_copilot.agents.publisher.upload import UploadSpec, build_args, parse_result, pick_upload_tool
from revops_copilot.common.a2aserver import TaskInput, TaskOutput, serve
from revops_copilot.common.bifrost import llm_refusal, tool_refusal, tool_result_text
from revops_copilot.common.config import AgentName
from revops_copilot.common.contracts import PublishResult

AGENT = AgentName.PUBLISHER


async def run(task: TaskInput) -> TaskOutput:
    pdfs = [f for f in task.files if f.media_type == "application/pdf"]
    if not pdfs:
        return TaskOutput(text="Send the PDF as a file part.")
    pdf = pdfs[0]
    session = task.session
    folder = session.settings.drive_folder_id

    llm = await session.chat_model()
    async with session.step("llm", "write the file description", surfaces="③④⑧"):
        try:
            description = (
                await llm.ainvoke(
                    [
                        SystemMessage("Write a one-sentence Google Drive file description. No quotes."),
                        HumanMessage(f"File: {pdf.name}\nContext: {task.text}"),
                    ]
                )
            ).content
        except openai.APIStatusError as e:
            raise llm_refusal(e) from e

    async with session.mcp() as mcp:
        async with session.step("mcp", f"open {session.mcp_path()}", surfaces="⑤") as note:
            listed = await mcp.list_tools()
            tool = pick_upload_tool(listed.tools, session.settings.drive_upload_tool)
            note["detail"] = f"{len(listed.tools)} tools, upload with {tool.name}"
        args = build_args(
            tool,
            UploadSpec(
                filename=pdf.name,
                data=pdf.data,
                media_type=pdf.media_type,
                folder_id=folder,
                description=str(description).strip(),
            ),
        )
        async with session.step("tool", f"{tool.name} {pdf.name} ({len(pdf.data) // 1024} KB)", surfaces="⑥⑦"):
            result = await mcp.call_tool(tool.name, args)
            # A refusal arrives as an isError result; raising here makes the step report it as failed.
            denied = tool_refusal(tool.name, result)
            if denied is not None:
                raise denied

    text = tool_result_text(result)
    if getattr(result, "isError", False):
        failed = PublishResult(tool=tool.name, raw=text[:2000])
        return TaskOutput(text=f"Upload failed: {text[:500]}", data={"publish": failed.model_dump()}, failed=True)

    file_id, link = parse_result(text, getattr(result, "structuredContent", None))
    published = PublishResult(file_id=file_id, link=link, tool=tool.name, raw=text[:2000])
    return TaskOutput(
        text=f"Uploaded {pdf.name} with {tool.name}: {link or file_id or 'no link returned'}",
        data={"publish": published.model_dump()},
    )


SKILLS = [
    pb.AgentSkill(
        id="publish-pdf",
        name="Publish PDF to Drive",
        description="Uploads a PDF into the configured Google Drive folder and returns its link.",
        tags=["google-drive", "upload"],
        examples=["Publish this report"],
    )
]


def main() -> None:
    serve(AGENT, "Uploads finished reports to Google Drive, via the Bifrost MCP Gateway.", SKILLS, run)


if __name__ == "__main__":
    main()
