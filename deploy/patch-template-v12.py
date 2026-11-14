#!/usr/bin/env python3
"""Patch a live agent yaml for threadz v0.2.0 (config v12), in place.

Usage: patch-template-v12.py AGENT.yaml
Writes AGENT.yaml.bak first, prints every change, touches nothing else
(dry_run included). A second run changes nothing.
"""
import re
import shutil
import sys

# (parent key or None for top level, key) the v12 config no longer accepts.
REMOVED = [
    (None, "seed_playbook"), (None, "learning"), (None, "review"),
    (None, "embedding"), ("limits", "explore_percent"),
    ("schedule", "study_slots"),
]
# Tool names the agent no longer has; a persona list item naming one is dropped.
DEAD_TOOLS = re.compile(
    r"\b(note_(write|read|search)|memory_\w+|read_own_metrics|read_own_actions"
    r"|playbook_\w+|strategy_write|knowledge_\w+)\b")


def indent(line):
    return len(line) - len(line.lstrip(" "))


def block_end(lines, i):
    """Index after the key at lines[i] and everything nested under it."""
    base, j = indent(lines[i]), i + 1
    while j < len(lines) and (not lines[j].strip() or indent(lines[j]) > base):
        j += 1
    while j > i + 1 and not lines[j - 1].strip():
        j -= 1  # leave trailing blank lines to the parent
    return j


def find(lines, key, lo, hi, ind):
    for i in range(lo, hi):
        if indent(lines[i]) == ind and re.match(re.escape(key) + r":(\s|$)", lines[i][ind:]):
            return i
    return -1


def patch(lines):
    log = []

    def drop(i, what):
        j = block_end(lines, i)
        log.append("removed %s (%d line%s)" % (what, j - i, "" if j - i == 1 else "s"))
        del lines[i:j]

    # study.web_search -> top-level web_search (moved, dedented by 2).
    s = find(lines, "study", 0, len(lines), 0)
    if s >= 0:
        e = block_end(lines, s)
        w = find(lines, "web_search", s + 1, e, 2)
        if w >= 0 and find(lines, "web_search", 0, len(lines), 0) < 0:
            moved = [l[2:] if l.strip() else l for l in lines[w:block_end(lines, w)]]
            lines[s:e] = moved
            log.append("moved study.web_search to top-level web_search; removed the rest of study:")
        else:
            drop(s, "study:")

    for parent, key in REMOVED:
        if parent is None:
            i = find(lines, key, 0, len(lines), 0)
        else:
            p = find(lines, parent, 0, len(lines), 0)
            i = -1 if p < 0 else find(lines, key, p + 1, block_end(lines, p), 2)
        if i >= 0:
            drop(i, key + ":" if parent is None else parent + "." + key)

    p = find(lines, "persona", 0, len(lines), 0)
    if p >= 0:
        i = p + 1
        end = block_end(lines, p)
        while i < end:
            if re.match(r"\s+- ", lines[i]) and DEAD_TOOLS.search(lines[i]):
                log.append("removed persona line naming a removed tool: " + lines[i].strip()[:80] + "...")
                del lines[i]
                end -= 1
            else:
                i += 1
    return log


def main():
    path = sys.argv[1]
    with open(path, encoding="utf-8", newline="") as f:
        text = f.read()
    lines = text.splitlines(keepends=True)
    if lines and not lines[-1].endswith("\n"):
        lines[-1] += "\n"
    log = patch(lines)
    if not log:
        print("no changes")
        return
    shutil.copyfile(path, path + ".bak")
    with open(path, "w", encoding="utf-8", newline="") as f:
        f.write("".join(lines))
    print("\n".join(log))
    print("backup: " + path + ".bak")


if __name__ == "__main__":
    main()
