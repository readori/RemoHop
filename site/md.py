"""A small Markdown to HTML converter for the RemoHop docs. Standard library only.

It covers what docs/*.md actually use, with GitHub's rendering as the model:
ATX headings (ids follow GitHub's slug rules, so existing #anchors keep
working), paragraphs, hard line breaks, **strong** and *emphasis*, `code`,
fenced and indented code blocks, GFM tables (with \\| inside cells), bullet
and ordered lists (nested, loose or tight, with tables and code inside an
item), [links](url), ![images](src), <https://autolinks>, blockquotes,
horizontal rules and backslash escapes.

Not supported, because the docs do not use them: raw HTML (it is escaped and
shown as text), setext headings, reference-style links, footnotes,
strikethrough and bare-URL autolinks.

Everything that comes from the Markdown source is HTML-escaped. Link targets
go through a caller-supplied function, which returns the new URL or None to
print the link text without a link.
"""
import html
import re
import unicodedata

FENCE = re.compile(r"^( {0,3})(`{3,}|~{3,})\s*([^`\s]*)[^`]*$")
HEADING = re.compile(r"^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$")
HR = re.compile(r"^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$")
QUOTE = re.compile(r"^ {0,3}> ?(.*)$")
LIST = re.compile(r"^( {0,3})([-*+]|\d{1,9}[.)])(?:([ \t]+)(.*))?$")
TABLE_SEP = re.compile(r"^ {0,3}\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$")
PUNCT = set("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~")


def slug(text):
    """GitHub's heading anchor: lower case, drop everything that is not a
    letter, mark, number, connector, space or hyphen, spaces to hyphens."""
    out = []
    for ch in text.strip().lower():
        cat = unicodedata.category(ch)
        if ch == " ":
            out.append("-")
        elif ch == "-" or cat[0] in "LMN" or cat == "Pc":
            out.append(ch)
    return "".join(out)


def esc(s):
    return html.escape(s, quote=False)


def attr(s):
    return html.escape(s, quote=True)


def plain(fragment):
    """Text content of a rendered inline HTML fragment."""
    return html.unescape(re.sub(r"<[^>]+>", "", fragment))


class Renderer:
    """link(url) -> url or None; image(src) -> (url, width, height) or None.
    heading_offset shifts heading levels (1 turns ## into <h3>)."""

    def __init__(self, link, image, heading_offset=0, code_attrs=' tabindex="0"'):
        self.link = link
        self.image = image
        self.offset = heading_offset
        self.code_attrs = code_attrs
        self.ids = {}
        self.title = None

    # ---- blocks ----

    def render(self, text):
        lines = text.replace("\r\n", "\n").expandtabs(4).split("\n")
        return "\n".join(self.blocks(lines))

    def blocks(self, lines, tight=False):
        out = []
        i, n = 0, len(lines)
        while i < n:
            line = lines[i]
            if not line.strip():
                i += 1
                continue
            m = FENCE.match(line)
            if m:
                i = self.fence(lines, i, m, out)
                continue
            if line.startswith("    "):
                code = []
                while i < n and (lines[i].startswith("    ") or not lines[i].strip()):
                    code.append(lines[i][4:])
                    i += 1
                while code and not code[-1].strip():
                    code.pop()
                out.append(f"<pre{self.code_attrs}><code>{esc(chr(10).join(code))}</code></pre>")
                continue
            m = HEADING.match(line)
            if m:
                out.append(self.heading(len(m.group(1)), m.group(2) or ""))
                i += 1
                continue
            if HR.match(line):
                out.append("<hr>")
                i += 1
                continue
            if QUOTE.match(line):
                inner = []
                while i < n and QUOTE.match(lines[i]):
                    inner.append(QUOTE.match(lines[i]).group(1))
                    i += 1
                out.append("<blockquote>\n" + "\n".join(self.blocks(inner)) + "\n</blockquote>")
                continue
            if self.is_table(lines, i):
                i = self.table(lines, i, out)
                continue
            if LIST.match(line):
                i = self.list(lines, i, out)
                continue
            para = []
            while i < n and lines[i].strip() and not (para and self.interrupts(lines, i)):
                para.append(lines[i])
                i += 1
            body = self.paragraph(para)
            out.append(body if tight else f"<p>{body}</p>")
        return out

    def interrupts(self, lines, i):
        line = lines[i]
        if FENCE.match(line) or HEADING.match(line) or HR.match(line) or QUOTE.match(line):
            return True
        if self.is_table(lines, i):
            return True
        m = LIST.match(line)
        # As in CommonMark, an ordered list interrupts a paragraph only from 1.
        return bool(m and m.group(4) and (not m.group(2)[0].isdigit() or m.group(2)[:-1] == "1"))

    def fence(self, lines, i, m, out):
        indent, marker, lang = len(m.group(1)), m.group(2), m.group(3)
        code = []
        i += 1
        while i < len(lines):
            close = re.match(r"^ {0,3}(" + re.escape(marker[0]) + r"{" + str(len(marker)) + r",})[ \t]*$", lines[i])
            if close:
                i += 1
                break
            line = lines[i]
            strip = len(line) - len(line.lstrip(" "))
            code.append(line[min(strip, indent):])
            i += 1
        cls = f' class="language-{attr(lang)}"' if lang else ""
        out.append(f"<pre{self.code_attrs}><code{cls}>{esc(chr(10).join(code))}</code></pre>")
        return i

    def heading(self, level, text):
        inner = self.inline(text.strip())
        if level == 1 and self.title is None:
            self.title = plain(inner)
        base = slug(plain(inner))
        count = self.ids.get(base, 0)
        self.ids[base] = count + 1
        hid = base if count == 0 else f"{base}-{count}"
        lv = min(6, level + self.offset)
        return f'<h{lv} id="{attr(hid)}">{inner}</h{lv}>'

    @staticmethod
    def is_table(lines, i):
        return "|" in lines[i] and i + 1 < len(lines) and "-" in lines[i + 1] and bool(TABLE_SEP.match(lines[i + 1]))

    @staticmethod
    def cells(row):
        row = row.strip()
        if row.startswith("|"):
            row = row[1:]
        if row.endswith("|") and not row.endswith("\\|"):
            row = row[:-1]
        parts, cur, k = [], "", 0
        while k < len(row):
            c = row[k]
            if c == "\\" and k + 1 < len(row) and row[k + 1] == "|":
                cur += "|"  # GFM: an escaped pipe is a literal pipe, also inside code
                k += 2
                continue
            if c == "|":
                parts.append(cur.strip())
                cur = ""
            else:
                cur += c
            k += 1
        parts.append(cur.strip())
        return parts

    def table(self, lines, i, out):
        head = self.cells(lines[i])
        aligns = []
        for spec in self.cells(lines[i + 1]):
            left, right = spec.startswith(":"), spec.endswith(":")
            aligns.append("center" if left and right else "right" if right else "left" if left else "")
        i += 2
        rows = []
        while i < len(lines) and lines[i].strip() and "|" in lines[i]:
            rows.append(self.cells(lines[i]))
            i += 1

        def cell(tag, text, k, scope=""):
            a = aligns[k] if k < len(aligns) else ""
            cls = f' class="{a}"' if a else ""  # no inline styles: the CSP forbids them
            return f"<{tag}{scope}{cls}>{self.inline(text)}</{tag}>"

        label = attr(" / ".join(plain(self.inline(h)) for h in head if h.strip()) or "table")
        parts = [f'<div class="table-wrap" tabindex="0" role="region" aria-label="{label}">', "<table>"]
        if any(h for h in head):
            parts.append("<thead><tr>" + "".join(cell("th", h, k, ' scope="col"') for k, h in enumerate(head)) + "</tr></thead>")
        parts.append("<tbody>")
        for r in rows:
            r = (r + [""] * len(head))[: len(head)]
            parts.append("<tr>" + "".join(cell("td", c, k) for k, c in enumerate(r)) + "</tr>")
        parts += ["</tbody>", "</table>", "</div>"]
        out.append("\n".join(parts))
        return i

    def list(self, lines, i, out):
        first = LIST.match(lines[i])
        ordered = first.group(2)[0].isdigit()
        start = int(first.group(2)[:-1]) if ordered else 1
        items, loose = [], False
        n = len(lines)
        while i < n:
            m = LIST.match(lines[i])
            if not m or m.group(2)[0].isdigit() != ordered:
                break
            spaces = m.group(3) or " "
            if len(spaces) > 4:
                spaces = " "
            offset = len(m.group(1)) + len(m.group(2)) + len(spaces)
            body = [m.group(4) or ""]
            if m.group(3) and len(m.group(3)) > 4:
                body = [" " * (len(m.group(3)) - 1) + (m.group(4) or "")]
            i += 1
            in_fence = bool(FENCE.match(body[0]))
            while i < n:
                line = lines[i]
                if not line.strip():
                    # A blank line continues the item only if indented content follows.
                    j = i
                    while j < n and not lines[j].strip():
                        j += 1
                    if j < n and len(lines[j]) - len(lines[j].lstrip(" ")) >= offset:
                        body.extend([""] * (j - i))
                        i = j
                        continue
                    break
                ind = len(line) - len(line.lstrip(" "))
                if ind >= offset:
                    body.append(line[offset:])
                elif in_fence:
                    body.append(line.lstrip(" "))
                elif LIST.match(line) or self.interrupts(lines, i) or not body[-1].strip():
                    break
                else:
                    body.append(line.strip())  # lazy continuation of a paragraph
                if FENCE.match(body[-1]):
                    in_fence = not in_fence
                i += 1
            if self.has_inner_blank(body):
                loose = True
            items.append(body)
            # A blank line between two items makes the list loose.
            if i < n and not lines[i].strip():
                j = i
                while j < n and not lines[j].strip():
                    j += 1
                nxt = LIST.match(lines[j]) if j < n else None
                if nxt and nxt.group(2)[0].isdigit() == ordered and len(nxt.group(1)) <= len(first.group(1)):
                    loose = True
                    i = j
                    continue
                break
        tag = "ol" if ordered else "ul"
        open_tag = f'<ol start="{start}">' if ordered and start != 1 else f"<{tag}>"
        parts = [open_tag]
        for body in items:
            inner = self.blocks(body, tight=not loose)
            if not loose:
                parts.append("<li>" + "\n".join(inner) + "</li>")
            else:
                parts.append("<li>\n" + "\n".join(inner) + "\n</li>")
        parts.append(f"</{tag}>")
        out.append("\n".join(parts))
        return i

    @staticmethod
    def has_inner_blank(body):
        fence = False
        seen_blank = False
        for line in body:
            if FENCE.match(line):
                fence = not fence
            if fence:
                continue
            if not line.strip():
                seen_blank = True
            elif seen_blank:
                return True
        return False

    def paragraph(self, lines):
        parts = []
        for k, line in enumerate(lines):
            last = k == len(lines) - 1
            s = line.lstrip(" ")
            if not last and (s.endswith("  ") or s.endswith("\\")):
                s = s[:-1] if s.endswith("\\") else s.rstrip(" ")
                parts.append(self.inline(s.rstrip(" ")) + "<br>")
            else:
                parts.append(self.inline(s.rstrip(" ")))
        return "\n".join(parts)

    # ---- inlines ----

    def inline(self, s):
        out = []
        i, n = 0, len(s)
        text = []

        def flush():
            if text:
                out.append(esc("".join(text)))
                text.clear()

        while i < n:
            c = s[i]
            if c == "\\" and i + 1 < n and s[i + 1] in PUNCT:
                text.append(s[i + 1])
                i += 2
                continue
            if c == "`":
                j = i
                while j < n and s[j] == "`":
                    j += 1
                run = s[i:j]
                k = j
                while True:
                    k = s.find(run, k)
                    if k < 0:
                        break
                    e = k + len(run)
                    if (k == 0 or s[k - 1] != "`") and (e >= n or s[e] != "`"):
                        break
                    k = e
                    while k < n and s[k] == "`":
                        k += 1
                if k < 0:
                    text.append(run)
                    i = j
                    continue
                code = s[j:k].replace("\n", " ")
                if len(code) > 2 and code[0] == " " and code[-1] == " " and code.strip():
                    code = code[1:-1]
                flush()
                out.append(f"<code>{esc(code)}</code>")
                i = k + len(run)
                continue
            if c == "<":
                m = re.match(r"<((?:https?|mailto):[^\s<>]+)>", s[i:])
                if m:
                    flush()
                    href = self.link(m.group(1))
                    label = esc(m.group(1))
                    out.append(f'<a href="{attr(href)}">{label}</a>' if href else label)
                    i += m.end()
                    continue
            if c == "!" and i + 1 < n and s[i + 1] == "[":
                parsed = self.bracket(s, i + 1)
                if parsed:
                    label, dest, end = parsed
                    flush()
                    img = self.image(dest)
                    alt = attr(plain(self.inline(label)))
                    if img:
                        src, w, h = img
                        out.append(f'<img src="{attr(src)}" alt="{alt}" width="{w}" height="{h}" loading="lazy" decoding="async">')
                    else:
                        out.append(esc(plain(self.inline(label))))
                    i = end
                    continue
            if c == "[":
                parsed = self.bracket(s, i)
                if parsed:
                    label, dest, end = parsed
                    flush()
                    href = self.link(dest)
                    inner = self.inline(label)
                    out.append(f'<a href="{attr(href)}">{inner}</a>' if href else inner)
                    i = end
                    continue
            if c in "*_":
                j = i
                while j < n and s[j] == c:
                    j += 1
                run = j - i
                done = False
                for k in (min(run, 2), 1) if run >= 2 else (1,):
                    close = self.find_close(s, i, k, c)
                    if close is not None:
                        flush()
                        inner = self.inline(s[i + k : close])
                        tag = "strong" if k == 2 else "em"
                        out.append(f"<{tag}>{inner}</{tag}>")
                        i = close + k
                        done = True
                        break
                if done:
                    continue
                text.append(s[i:j])
                i = j
                continue
            text.append(c)
            i += 1
        flush()
        return "".join(out)

    @staticmethod
    def find_close(s, i, k, c):
        """Position of the delimiter run that closes an emphasis of length k
        opening at s[i]. Code spans are skipped; a run of the other length is
        nested emphasis and skipped too (``*a **b** c*``)."""
        n = len(s)
        start = i + k
        if start >= n or s[start].isspace():
            return None
        if c == "_" and i > 0 and s[i - 1].isalnum():
            return None  # intraword underscores are not emphasis
        j = start
        while j < n:
            ch = s[j]
            if ch == "\\":
                j += 2
                continue
            if ch == "`":
                r = j
                while r < n and s[r] == "`":
                    r += 1
                e = s.find(s[j:r], r)
                j = (e + (r - j)) if e >= 0 else r
                continue
            if ch != c:
                j += 1
                continue
            r = j
            while r < n and s[r] == c:
                r += 1
            run = r - j
            closes = j > start and not s[j - 1].isspace() and not (c == "_" and r < n and s[r].isalnum())
            if closes and run == k:
                return j
            if closes and run == 3:
                return j + 3 - k  # "**a *b***" / "*a **b***"
            j = r
        return None

    @staticmethod
    def bracket(s, i):
        """Parse [label](dest "title") at s[i] == '['. Returns (label, dest, end)."""
        depth, j, n = 0, i, len(s)
        while j < n:
            ch = s[j]
            if ch == "\\":
                j += 2
                continue
            if ch == "`":
                r = j
                while r < n and s[r] == "`":
                    r += 1
                e = s.find(s[j:r], r)
                j = (e + (r - j)) if e >= 0 else r
                continue
            if ch == "[":
                depth += 1
            elif ch == "]":
                depth -= 1
                if depth == 0:
                    break
            j += 1
        if j >= n or j + 1 >= n or s[j + 1] != "(":
            return None
        label = s[i + 1 : j]
        m = re.match(r"\(\s*(<[^<>\n]*>|[^\s()<>]*(?:\([^\s()<>]*\)[^\s()<>]*)*)(?:\s+(\"[^\"]*\"|'[^']*'))?\s*\)", s[j + 1 :])
        if not m:
            return None
        dest = m.group(1)
        if dest.startswith("<"):
            dest = dest[1:-1]
        dest = re.sub(r"\\([" + re.escape("".join(sorted(PUNCT))) + r"])", r"\1", dest)
        return label, dest, j + 1 + m.end()
