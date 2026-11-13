"""Render a ReportDraft + SupportMetrics into a PDF. Pure local work, no Bifrost hop."""

from __future__ import annotations

import io
from datetime import UTC, datetime

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402
from reportlab.lib import colors  # noqa: E402
from reportlab.lib.pagesizes import A4  # noqa: E402
from reportlab.lib.styles import getSampleStyleSheet  # noqa: E402
from reportlab.lib.units import cm  # noqa: E402
from reportlab.platypus import (  # noqa: E402
    Image,
    ListFlowable,
    ListItem,
    Paragraph,
    SimpleDocTemplate,
    Spacer,
    Table,
    TableStyle,
)

from revops_copilot.common.contracts import ReportDraft, SupportMetrics  # noqa: E402

INK = "#1f2937"
ACCENT = "#2563eb"


def _bar_chart(labels: list[str], values: list[int], title: str) -> io.BytesIO | None:
    if not labels:
        return None
    fig, ax = plt.subplots(figsize=(6.4, 2.6), dpi=150)
    ax.bar(labels, values, color=ACCENT, width=0.6)
    ax.set_title(title, fontsize=10, color=INK, loc="left")
    ax.spines[["top", "right"]].set_visible(False)
    ax.tick_params(labelsize=8, colors=INK)
    for i, v in enumerate(values):
        ax.annotate(str(v), (i, v), ha="center", va="bottom", fontsize=7, color=INK)
    fig.tight_layout()
    buf = io.BytesIO()
    fig.savefig(buf, format="png")
    plt.close(fig)
    buf.seek(0)
    return buf


def _escape(text: str) -> str:
    return text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def render(draft: ReportDraft, metrics: SupportMetrics) -> bytes:
    styles = getSampleStyleSheet()
    body = styles["BodyText"]
    out = io.BytesIO()
    doc = SimpleDocTemplate(out, pagesize=A4, leftMargin=2 * cm, rightMargin=2 * cm, title=draft.title)
    story: list = [
        Paragraph(_escape(draft.title), styles["Title"]),
        Paragraph(f"Generated {datetime.now(UTC):%Y-%m-%d %H:%M UTC} by revops-copilot via Bifrost", styles["Italic"]),
        Spacer(1, 0.4 * cm),
        Paragraph("Executive summary", styles["Heading2"]),
        Paragraph(_escape(draft.executive_summary), body),
        Spacer(1, 0.3 * cm),
    ]

    kpis = [
        ["Issues", "Resolved", "SLA breaches", "Median first response"],
        [
            str(metrics.total_issues),
            str(metrics.resolved_issues),
            str(metrics.sla_breaches),
            "n/a" if metrics.median_first_response_hours is None else f"{metrics.median_first_response_hours:.1f} h",
        ],
    ]
    table = Table(kpis, hAlign="LEFT")
    table.setStyle(
        TableStyle(
            [
                ("FONTNAME", (0, 0), (-1, 0), "Helvetica-Bold"),
                ("TEXTCOLOR", (0, 0), (-1, -1), colors.HexColor(INK)),
                ("LINEBELOW", (0, 0), (-1, 0), 0.5, colors.HexColor(ACCENT)),
                ("BOTTOMPADDING", (0, 0), (-1, -1), 6),
            ]
        )
    )
    story += [table, Spacer(1, 0.4 * cm)]

    for chart in (
        _bar_chart([m.month for m in metrics.issues_by_month], [m.count for m in metrics.issues_by_month], "Issues by month"),
        _bar_chart(list(metrics.issues_by_priority), list(metrics.issues_by_priority.values()), "Issues by priority"),
    ):
        if chart is not None:
            story += [Image(chart, width=16 * cm, height=6.5 * cm), Spacer(1, 0.2 * cm)]

    for section in draft.sections:
        story += [Paragraph(_escape(section.heading), styles["Heading2"]), Paragraph(_escape(section.body), body)]

    if metrics.top_accounts:
        rows = [["Account", "Open issues", "SLA breaches", "Note"]] + [
            [a.account, str(a.open_issues), str(a.sla_breaches), Paragraph(_escape(a.note), body)]
            for a in metrics.top_accounts
        ]
        accounts = Table(rows, hAlign="LEFT", colWidths=[4.5 * cm, 2.4 * cm, 2.6 * cm, 7.5 * cm])
        accounts.setStyle(TableStyle([("FONTNAME", (0, 0), (-1, 0), "Helvetica-Bold"), ("VALIGN", (0, 0), (-1, -1), "TOP")]))
        story += [Paragraph("Accounts to watch", styles["Heading2"]), accounts]

    for heading, items in (("Risks", draft.risks), ("Recommendations", draft.recommendations), ("Data gaps", metrics.data_gaps)):
        if items:
            story += [
                Paragraph(heading, styles["Heading2"]),
                ListFlowable([ListItem(Paragraph(_escape(i), body)) for i in items], bulletType="bullet"),
            ]

    doc.build(story)
    return out.getvalue()
