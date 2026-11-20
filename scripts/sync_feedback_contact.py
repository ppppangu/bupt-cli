#!/usr/bin/env python3
"""Synchronize the public contact in README from the hardcoded Go constant."""
from pathlib import Path
import re

root = Path(__file__).resolve().parents[1]
source = (root / "internal/feedback/feedback.go").read_text(encoding="utf-8")
email = re.search(r'^const Email = "([A-Za-z0-9._+@-]+)"$', source, re.MULTILINE)[1]
readme = root / "README.md"
body = readme.read_text(encoding="utf-8")
line = f"**联系邮箱** [{email}](mailto:{email})"
body, count = re.subn(
    r"(<!-- feedback-contact:start -->\n).*?(\n<!-- feedback-contact:end -->)",
    lambda match: match[1] + line + match[2], body, flags=re.DOTALL,
)
if count != 1:
    raise SystemExit("README must contain one feedback-contact block")
readme.write_text(body, encoding="utf-8")
