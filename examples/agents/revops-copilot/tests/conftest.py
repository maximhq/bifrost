from __future__ import annotations

from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def _no_dotenv(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    # Settings read .env from the working directory, and monkeypatch.delenv cannot
    # remove a value that comes from that file. Run every test in an empty
    # directory so a filled-in .env next to the checkout never leaks in.
    monkeypatch.chdir(tmp_path)
