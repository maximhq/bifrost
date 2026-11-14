"""Typed payloads the agents pass to each other as A2A data parts."""

from __future__ import annotations

from datetime import date

from pydantic import BaseModel, Field, model_validator


class ReportRequest(BaseModel):
    """The period a report covers: any date range, inclusive."""

    start: date
    end: date
    label: str = Field(default="", description="Short name, e.g. 'Q3 2026' or '2026-11-10'")
    audience: str = "leadership"
    focus: str = Field(default="", description="Anything the user asked to emphasise")

    @model_validator(mode="after")
    def _period(self) -> ReportRequest:
        if self.end < self.start:
            raise ValueError("end is before start")
        if not self.label:
            self.label = self.start.isoformat() if self.start == self.end else f"{self.start} to {self.end}"
        return self

    @classmethod
    def quarter(cls, quarter: int, year: int, **kw: str) -> ReportRequest:
        first = date(year, 3 * (quarter - 1) + 1, 1)
        nxt = date(year + 1, 1, 1) if quarter == 4 else date(year, 3 * quarter + 1, 1)
        return cls(start=first, end=date.fromordinal(nxt.toordinal() - 1), label=f"Q{quarter} {year}", **kw)


class MonthCount(BaseModel):
    month: str = Field(description="YYYY-MM")
    count: int


class AccountHealth(BaseModel):
    account: str
    open_issues: int
    sla_breaches: int
    note: str = ""


class SupportMetrics(BaseModel):
    """support-analyst's answer, pulled from Pylon through Bifrost MCP."""

    quarter_label: str
    total_issues: int
    resolved_issues: int
    median_first_response_hours: float | None = None
    sla_breaches: int = 0
    issues_by_month: list[MonthCount] = Field(default_factory=list)
    issues_by_priority: dict[str, int] = Field(default_factory=dict)
    top_accounts: list[AccountHealth] = Field(default_factory=list)
    themes: list[str] = Field(default_factory=list, description="Recurring topics, one line each")
    data_gaps: list[str] = Field(default_factory=list, description="What the tools could not answer")


class ReportSection(BaseModel):
    heading: str
    body: str


class ReportDraft(BaseModel):
    """report-writer's narrative, rendered into the PDF."""

    title: str
    executive_summary: str
    sections: list[ReportSection]
    risks: list[str] = Field(default_factory=list)
    recommendations: list[str] = Field(default_factory=list)


class PublishResult(BaseModel):
    file_id: str = ""
    link: str = ""
    tool: str = ""
    raw: str = ""
