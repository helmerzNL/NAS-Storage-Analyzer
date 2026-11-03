"""The README is a page people read first, in two languages, with pictures. It rots quietly: a renamed variable, a moved
document, a screenshot nobody regenerated, a Dutch page that gained a section the English one never got.

README.md is Dutch, README.en.md is English. These tests read both as text and fail when they drift apart or point at
something that is gone: the same structure in both, every relative link and image exists, every anchor lands on a heading
(in the README and in the documents it links to), every screenshot has a twin in the other language and is used, and every
`NASSA_` variable the README names is one the application reads.

Run: python -m unittest discover -s scripts -p "test_readme.py" -v
"""
import re
import unittest
from pathlib import Path
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[1]
NL = ROOT / "README.md"
EN = ROOT / "README.en.md"
TITLE = "# NASSA - NAS Storage Analyzer"
SHOTS = ROOT / "docs" / "screenshots"

# Named in the README as part of how to build or test it, not as settings of the running application.
NOT_IN_ENV_EXAMPLE = {"NASSA_TEST_DSN", "NASSA_REQUIRE_DB", "NASSA_DB_DSN"}


def lines_outside_code(text: str):
    """Yield (line number, line) for the lines that are not inside a fenced code block."""
    fence = None
    for n, line in enumerate(text.splitlines(), 1):
        m = re.match(r"^\s*(```+|~~~+)", line)
        if m:
            if fence is None:
                fence = m.group(1)[0]
            elif m.group(1)[0] == fence:
                fence = None
            continue
        if fence is None:
            yield n, line


def headings(text: str):
    """[(level, title)] of every Markdown heading outside code blocks."""
    out = []
    for _, line in lines_outside_code(text):
        m = re.match(r"^(#{1,6})\s+(.*?)\s*#*$", line)
        if m:
            out.append((len(m.group(1)), m.group(2)))
    return out


def slug(title: str) -> str:
    """GitHub's anchor for a heading: lower case, only letters, digits, spaces, `-` and `_` kept, spaces become `-`."""
    t = re.sub(r"`", "", title).lower()
    t = re.sub(r"[^\w\- ]", "", t)
    return t.replace(" ", "-")


def anchors_of(path: Path):
    seen, out = {}, set()
    for _, title in headings(path.read_text(encoding="utf-8")):
        s = slug(title)
        n = seen.get(s, 0)
        seen[s] = n + 1
        out.add(s if n == 0 else f"{s}-{n}")
    return out


def links(text: str):
    """Every Markdown link and image target, and every `src`/`href` of inline HTML, outside code blocks."""
    out = []
    for n, line in lines_outside_code(text):
        # `code spans` may hold text that only looks like a link.
        bare = re.sub(r"`[^`]*`", "", line)
        for m in re.finditer(r"!?\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)", bare):
            out.append((n, m.group(1)))
        for m in re.finditer(r"""\b(?:src|href)=["']([^"']+)["']""", bare):
            out.append((n, m.group(1)))
    return out


def relative(target: str) -> bool:
    return not re.match(r"^(?:[a-z][a-z0-9+.-]*:|//|#)", target, re.I) or target.startswith("#")


class ReadmeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = {"nl": NL.read_text(encoding="utf-8"), "en": EN.read_text(encoding="utf-8")}

    def test_both_readmes_have_the_product_name_as_their_title(self):
        for lang, text in self.text.items():
            top = [title for level, title in headings(text) if level == 1]  # not the `# comment` lines inside code blocks
            self.assertEqual(top, [TITLE[2:]], f"{lang}: the one top-level heading must be {TITLE[2:]!r}")

    def test_each_readme_points_at_the_other(self):
        self.assertIn("(README.en.md)", self.text["nl"])
        self.assertIn("(README.md)", self.text["en"])

    def test_both_readmes_have_the_same_structure(self):
        shape = {lang: [level for level, _ in headings(text)] for lang, text in self.text.items()}
        self.assertEqual(shape["nl"], shape["en"], "the heading levels differ: a section is missing in one language")

    def test_the_contents_list_matches_the_sections(self):
        for lang, text in self.text.items():
            listed = re.findall(r"^- \[[^\]]+\]\(#([^)]+)\)$", text.split("\n## ", 2)[1], re.M)
            top = [slug(t) for level, t in headings(text) if level == 2 and slug(t) not in ("inhoud", "contents")]
            self.assertEqual(listed, top, f"{lang}: the contents list does not match the ## sections, in order")

    def test_both_readmes_link_to_the_same_documents_and_images(self):
        def targets(lang):
            out = set()
            for _, t in links(self.text[lang]):
                if not relative(t) or t.startswith("#"):
                    continue
                out.add(unquote(t.split("#")[0]).replace("/nl/", "/<lang>/").replace("/en/", "/<lang>/"))
            out.discard("README.md")
            out.discard("README.en.md")
            return out

        nl, en = targets("nl"), targets("en")
        self.assertEqual(nl - en, set(), "linked only from the Dutch README")
        self.assertEqual(en - nl, set(), "linked only from the English README")

    def test_every_relative_link_and_image_exists(self):
        for lang, path in (("nl", NL), ("en", EN)):
            for n, target in links(self.text[lang]):
                if not relative(target) or target.startswith("#"):
                    continue
                file = unquote(target.split("#")[0])
                self.assertTrue((ROOT / file).exists(), f"{path.name}:{n} points at {target}, which does not exist")

    def test_every_anchor_lands_on_a_heading(self):
        for lang, path in (("nl", NL), ("en", EN)):
            for n, target in links(self.text[lang]):
                if not relative(target) or "#" not in target:
                    continue
                file, anchor = target.split("#", 1)
                doc = path if file == "" else ROOT / unquote(file)
                if doc.suffix != ".md":
                    continue
                self.assertIn(anchor, anchors_of(doc), f"{path.name}:{n} points at {target}, and {doc.name} has no such heading")

    def test_every_link_into_a_readme_from_the_documents_lands_on_a_heading(self):
        docs = [ROOT / "AGENTS.md", *sorted((ROOT / "docs").rglob("*.md"))]
        for doc in docs:
            for n, target in links(doc.read_text(encoding="utf-8")):
                m = re.match(r"^(?:\.\./|\./)*(README(?:\.en)?\.md)#(.+)$", target)
                if m:
                    self.assertIn(m.group(2), anchors_of(ROOT / m.group(1)), f"{doc.relative_to(ROOT)}:{n} points at {target}")

    def test_every_screenshot_has_a_twin_in_the_other_language_and_is_used(self):
        names = {lang: {p.name for p in (SHOTS / lang).glob("*.png")} for lang in ("nl", "en")}
        self.assertTrue(names["nl"], "there are no screenshots")
        self.assertEqual(names["nl"], names["en"], "a screenshot exists in only one language")
        for lang in ("nl", "en"):
            used = set(re.findall(rf"docs/screenshots/{lang}/([\w.-]+\.png)", self.text[lang]))
            self.assertEqual(used, names[lang], f"{lang}: screenshots that exist but are not shown, or are shown but do not exist")

    def test_every_image_has_a_description(self):
        for lang, text in self.text.items():
            for n, line in lines_outside_code(text):
                for m in re.finditer(r"<img\b[^>]*>", line):
                    tag = m.group(0)
                    if "favicon.svg" in tag:
                        continue  # the logo next to the title is decoration; the title says the same
                    alt = re.search(r'alt="([^"]*)"', tag)
                    self.assertTrue(alt and len(alt.group(1)) >= 20, f"{lang}:{n}: an image without a real description: {tag[:80]}")

    def test_the_variables_it_names_are_the_ones_the_application_reads(self):
        env_example = (ROOT / ".env.example").read_text(encoding="utf-8")
        known = set(re.findall(r"^#?\s*([A-Z][A-Z0-9_]+)=", env_example, re.M)) | NOT_IN_ENV_EXAMPLE
        for lang, text in self.text.items():
            code = "\n".join(line for _, line in lines_outside_code(text)) + "\n" + "\n".join(re.findall(r"```.*?```", text, re.S))
            for name in sorted(set(re.findall(r"\bNASSA_[A-Z0-9_]+\b", code))):
                self.assertIn(name, known, f"{lang}: {name} is named in the README but is not in .env.example")

    def test_the_old_variable_names_only_appear_as_the_rename_instruction(self):
        # `NASA_` followed by a letter would be a variable that no longer exists; `NASA_*`, `^NASA_` and "NASA_" alone are the rename itself.
        for lang, text in self.text.items():
            self.assertEqual(re.findall(r"NASA_[A-Z0-9]\w*", text), [], f"{lang}: an old NASA_ variable name")


if __name__ == "__main__":
    unittest.main()
