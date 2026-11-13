"""A small LangGraph tool loop over a Bifrost MCP session.

    START -> llm --(tool calls?)--> tools -> llm ... -> END

The graph is explicit, with no prebuilt agent, so each Bifrost touchpoint is visible:

* `llm` calls the LLM Gateway (surfaces 3, 4, 8).
* `tools` calls the MCP Gateway (surfaces 6, 7). A refusal stops the loop
  with a PolicyDenied instead of being fed back to the model to retry around.
"""

from __future__ import annotations

import json
from collections.abc import AsyncIterator
from contextlib import AsyncExitStack, asynccontextmanager
from typing import Any

import openai
from langchain_core.messages import AIMessage, BaseMessage, SystemMessage, ToolMessage
from langchain_core.tools import BaseTool
from langchain_mcp_adapters.tools import load_mcp_tools
from langchain_openai import ChatOpenAI
from langgraph.graph import END, START, MessagesState, StateGraph
from mcp import ClientSession

from revops_copilot.common.bifrost import BifrostSession, llm_refusal, tool_refusal, tool_result_text
from revops_copilot.common.policy import PolicyDenied

# Each raw tool result the model sees. Big dumps make every next decision slow.
MAX_TOOL_RESULT_CHARS = 6_000


async def discover_tools(session: ClientSession) -> list[BaseTool]:
    """List the tools this identity may see on Bifrost's MCP server (surface 5)."""
    return await load_mcp_tools(session)


@asynccontextmanager
async def bifrost_tools(
    session: BifrostSession, *, required: bool = True
) -> AsyncIterator[tuple[ClientSession | None, list[BaseTool]]]:
    """Open Bifrost's MCP server and list the granted tools, reporting both.

    required=False: an identity with no MCP access gets (None, []) and the agent
    carries on without tools; otherwise the refusal (surface 5) propagates.
    """
    path = session.mcp_path()
    async with AsyncExitStack() as stack:
        try:
            mcp = await stack.enter_async_context(session.mcp())
        except PolicyDenied as denied:
            if required:
                raise
            await session.progress("mcp", f"open {path}", detail=f"no MCP access ({denied.status})",
                                   surfaces="⑤", status="fail")
            yield None, []
            return
        async with session.step("mcp", f"open {path}", surfaces="⑤") as note:
            tools = await discover_tools(mcp)
            note["detail"] = f"{len(tools)} tools visible"
        yield mcp, tools


def _args(args: dict[str, Any]) -> str:
    # Names only: the step's start event goes out before the MCP input guardrail runs,
    # so a value here (a secret, personal data) would reach the progress stream and logs.
    text = ", ".join(args)
    return f"({text if len(text) <= 90 else text[:87] + '...'})"


async def call_mcp_tool(
    mcp: ClientSession, name: str, args: dict[str, Any], reporter: BifrostSession | None = None
) -> tuple[str, bool]:
    """One MCP tool call through Bifrost (surfaces 6/7): (result text, is_error).

    A Bifrost refusal raises PolicyDenied instead of being returned to the model,
    so an agent cannot retry around a policy decision.
    """

    async def call() -> tuple[str, bool]:
        result = await mcp.call_tool(name, args)
        denied = tool_refusal(name, result)
        if denied is not None:
            raise denied
        text = tool_result_text(result) or json.dumps(getattr(result, "structuredContent", None) or {}, default=str)
        return text[:MAX_TOOL_RESULT_CHARS], bool(getattr(result, "isError", False))

    if reporter is None:
        return await call()
    async with reporter.step("tool", f"{name} {_args(args)}", surfaces="⑥⑦") as note:
        text, error = await call()
        note["detail"] = f"{'tool error' if error else 'ok'} · {len(text):,} chars"
    return text, error


class ToolLoopExhausted(RuntimeError):
    """The model still wanted tools after max_steps, so the answer would be built from incomplete facts."""


def build_tool_loop(
    llm: ChatOpenAI,
    session: ClientSession,
    tools: list[BaseTool],
    system_prompt: str,
    *,
    max_steps: int = 12,
    reporter: BifrostSession | None = None,
) -> Any:
    bound = llm.bind_tools(tools) if tools else llm
    allowed = {t.name for t in tools}

    @asynccontextmanager
    async def step(kind: str, title: str, surfaces: str = "") -> AsyncIterator[dict[str, str]]:
        if reporter is None:
            yield {"detail": ""}
            return
        async with reporter.step(kind, title, surfaces=surfaces) as note:
            yield note

    async def call_llm(state: MessagesState) -> dict[str, list[BaseMessage]]:
        messages = state["messages"]
        if not messages or not isinstance(messages[0], SystemMessage):
            messages = [SystemMessage(system_prompt), *messages]
        steps = sum(1 for m in messages if isinstance(m, AIMessage))
        if steps >= max_steps:
            raise ToolLoopExhausted(f"tool step limit reached after {max_steps} steps; the facts gathered are incomplete")
        async with step("llm", f"decide next step ({steps + 1}/{max_steps})", "③④⑧") as note:
            try:
                reply = await bound.ainvoke(messages)
            except openai.APIStatusError as e:
                raise llm_refusal(e) from e
            calls = getattr(reply, "tool_calls", None) or []
            note["detail"] = ", ".join(c["name"] for c in calls) if calls else "answer ready"
        return {"messages": [reply]}

    async def call_tools(state: MessagesState) -> dict[str, list[BaseMessage]]:
        last = state["messages"][-1]
        assert isinstance(last, AIMessage)
        out: list[BaseMessage] = []
        for call in last.tool_calls:
            name = call["name"]
            if name not in allowed:
                # The model invented a tool name; Bifrost never offered it.
                out.append(ToolMessage(f"unknown tool {name}", tool_call_id=call["id"], status="error"))
                continue
            text, error = await call_mcp_tool(session, name, call["args"], reporter)
            out.append(ToolMessage(text, tool_call_id=call["id"], status="error" if error else "success"))
        return {"messages": out}

    def route(state: MessagesState) -> str:
        last = state["messages"][-1]
        return "tools" if isinstance(last, AIMessage) and last.tool_calls else END

    graph = StateGraph(MessagesState)
    graph.add_node("llm", call_llm)
    graph.add_node("tools", call_tools)
    graph.add_edge(START, "llm")
    graph.add_conditional_edges("llm", route, {"tools": "tools", END: END})
    graph.add_edge("tools", "llm")
    return graph.compile()


async def structured(llm: ChatOpenAI, schema: type, messages: list[BaseMessage]) -> Any:
    """Ask for a typed answer. Function calling is the most portable shape
    across the providers Bifrost routes to."""
    try:
        return await llm.with_structured_output(schema, method="function_calling").ainvoke(messages)
    except openai.APIStatusError as e:
        raise llm_refusal(e) from e
