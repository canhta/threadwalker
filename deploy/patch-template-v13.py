#!/usr/bin/env python3
"""Patch a live agent yaml for threadz v0.3.0 (config v13), in place.

Usage: patch-template-v13.py AGENT.yaml [TEMPLATE.yaml]
TEMPLATE.yaml defaults to templates/viral.agent.yaml next to this script.

v0.3.0 replaces the tools read_feed, read_topic, search and profile_posts with
find_posts, takes reply's target (a p ref) in place of target_url, target_id
and target_username, and post's trend in place of trend_ref. It also cuts the
persona: each rule now lives in one section, and the agent prompt no longer
carries style_rules, style_avoid, bad_examples or critic_rules.

It also refuses the keys older templates carry: the second writer model and the
study, playbook, learning and owner-message settings (REMOVED_KEYS).

This script does three things to AGENT.yaml:
 1. Removes those keys.
 2. Rewrites the persona sentences that name a removed tool or argument.
 3. Replaces the whole persona: block with the template's, but only when the
    live block (after step 2) is exactly the v0.2.1 repo persona, checked by
    sha256. A block the owner edited is left alone with a warning: merge it by
    hand.
It writes AGENT.yaml.bak first, prints every change and touches nothing else
(dry_run included). A second run changes nothing. A line that still names a
removed tool or argument is printed as a warning: edit it by hand.
"""
import hashlib
import os
import re
import shutil
import sys

# (old, new): the sentences of the v0.2.1 template that name a removed tool or argument.
REWRITES = [
    ("Đọc read_feed, rồi read_topic của 2–3 trend đang hot nhất (heat cao, nhiều bài) để tìm bài đang lên mà reply",
     "Gọi find_posts với source feed, rồi find_posts với source topic và topic là ref (như t1) của 2–3 trend đang hot nhất (heat cao, nhiều bài) để tìm bài đang lên mà reply"),
    ("post cần trend_ref là ref (như r3, không chép URL) của một mục fetch_trends, web_search, read_feed hoặc read_topic vừa trả về trong lần chạy này",
     "post cần trend là ref (như t3 hoặc p12, không chép URL) của một mục fetch_trends, web_search hoặc find_posts vừa trả về"),
    ("chọn trong bài read_feed hoặc read_topic vừa trả về; không gửi target_id, target_url là ref của bài (như r3), kèm angle",
     "chọn trong danh sách bài ứng viên hoặc bài find_posts vừa trả về; target là ref của bài (như p12), kèm angle"),
]
# Dotted paths of the keys older templates carry and v0.3.0 refuses as unknown: the second
# writer model, and the study, playbook and learning settings and the owner copy of v0.1 and v0.2.
REMOVED_KEYS = ["writer.post_llm", "study", "seed_playbook", "learning", "limits.explore_percent",
                "schedule.study_slots", "telegram.digest_hour", "telegram.messages"]
REMOVED = re.compile(r"\b(read_feed|read_topic|profile_posts|target_url|target_id|target_username|trend_ref)\b")

# sha256 of the persona: block of templates/viral.agent.yaml at v0.2.1 (after the REWRITES above), the only
# block this script replaces. The Go test checks it against the v0.2.1 release tag.
OLD_PERSONA_SHA256 = "7d37b4ca52ac78f836b7c54a43abaad05e194fdb489cb5f87303d22c7a212214"
DEFAULT_TEMPLATE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "templates", "viral.agent.yaml")


def persona_span(lines):
    """Line range of the top-level persona: block, without trailing blank lines; None when absent."""
    start = next((i for i, l in enumerate(lines) if l.startswith("persona:")), None)
    if start is None:
        return None
    end = next((i for i in range(start + 1, len(lines)) if lines[i].strip() and lines[i][0] not in " \t#"), len(lines))
    while end > start + 1 and not lines[end - 1].strip():
        end -= 1
    return start, end


def replace_persona(text, template_text, log):
    lines = text.splitlines(keepends=True)
    span = persona_span(lines)
    new_span = persona_span(template_text.splitlines(keepends=True))
    if span is None or new_span is None:
        log.append("WARNING no persona: block in the live yaml or the template; edit by hand")
        return text
    new = "".join(template_text.splitlines(keepends=True)[new_span[0]:new_span[1]])
    old = "".join(lines[span[0]:span[1]])
    if old == new:
        return text
    if hashlib.sha256(old.encode("utf-8")).hexdigest() != OLD_PERSONA_SHA256:
        log.append("WARNING persona: block differs from the known v0.2.1 persona (edited by hand?); left unchanged. Merge templates/viral.agent.yaml by hand")
        return text
    log.append("replaced persona: block (%d bytes -> %d bytes)" % (len(old.encode("utf-8")), len(new.encode("utf-8"))))
    return "".join(lines[:span[0]]) + new + "".join(lines[span[1]:])


def key_span(lines, path):
    """Line range of the key at a dotted path (key line to its last nested line), with the comment
    lines right above it; None when absent. Indentation decides what is nested."""
    lo, hi = 0, len(lines)
    for key in path.split("."):
        found = indent = None
        for i in range(lo, hi):
            body = lines[i].lstrip()
            if not body.strip() or body.startswith("#"):
                continue
            n = len(lines[i]) - len(body)
            indent = n if indent is None else indent
            if n == indent and body.startswith(key + ":"):
                found = i
                break
        if found is None:
            return None
        last = found
        for i in range(found + 1, hi):
            body = lines[i].lstrip()
            n = len(lines[i]) - len(body)
            if body.strip() and n <= indent and not (n == indent and body.startswith("- ")):
                break
            if body.strip():
                last = i
        lo, hi = found + 1, last + 1
    start = found
    while start > 0 and lines[start - 1].lstrip().startswith("#") and len(lines[start - 1]) - len(lines[start - 1].lstrip()) == indent:
        start -= 1
    return start, last + 1


def remove_keys(text, log):
    """Drop every key v0.3.0 refuses as unknown (REMOVED_KEYS)."""
    for path in REMOVED_KEYS:
        lines = text.splitlines(keepends=True)
        span = key_span(lines, path)
        if span:
            log.append("removed %s (%d lines)" % (path, span[1] - span[0]))
            text = "".join(lines[:span[0]] + lines[span[1]:])
    return text


def patch(text, template_text):
    log = []
    text = remove_keys(text, log)
    for old, new in REWRITES:
        if old in text:
            text = text.replace(old, new)
            log.append("rewrote: " + old[:60] + "...")
    text = replace_persona(text, template_text, log)
    for number, line in enumerate(text.splitlines(), 1):
        if REMOVED.search(line):
            log.append("WARNING line %d still names a removed tool or argument, edit it by hand: %s..." % (number, line.strip()[:70]))
    return text, log


def main():
    path = sys.argv[1]
    template = sys.argv[2] if len(sys.argv) > 2 else DEFAULT_TEMPLATE
    with open(path, encoding="utf-8", newline="") as f:
        text = f.read()
    with open(template, encoding="utf-8", newline="") as f:
        template_text = f.read()
    patched, log = patch(text, template_text)
    if patched == text and not log:
        print("no changes")
        return
    if patched != text:
        shutil.copyfile(path, path + ".bak")
        with open(path, "w", encoding="utf-8", newline="") as f:
            f.write(patched)
        log.append("backup: " + path + ".bak")
    print("\n".join(log))


if __name__ == "__main__":
    main()
