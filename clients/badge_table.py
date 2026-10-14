"""Print a Markdown compatibility table from the JUnit files of the clients workflow."""

import pathlib
import re
import sys
import xml.etree.ElementTree as ET


def status(path):
    if not path.exists():
        return "⚪"
    root = ET.parse(path).getroot()
    suites = [root] if root.tag == "testsuite" else root.iter("testsuite")
    failed = sum(int(s.get("failures", 0)) + int(s.get("errors", 0)) for s in suites)
    return "❌" if failed else "✅"


def main(results_dir):
    cells = {}
    for d in pathlib.Path(results_dir).iterdir():
        m = re.fullmatch(r"junit-(\w+)-(\w+)-pg(\d+)", d.name)
        if m:
            cells[m.groups()] = status(d / "junit.xml")
    columns = sorted({(mode, pg) for _, mode, pg in cells})
    print("| Language | " + " | ".join(f"{mode} / pg{pg}" for mode, pg in columns) + " |")
    print("| --- |" + " --- |" * len(columns))
    for lang in sorted({lang for lang, _, _ in cells}):
        row = [cells.get((lang, mode, pg), "⚪") for mode, pg in columns]
        print(f"| {lang} | " + " | ".join(row) + " |")


if __name__ == "__main__":
    main(sys.argv[1])
