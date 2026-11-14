#!/usr/bin/env python3
"""Patch a live agent yaml for threadz v0.3.0 (config v13), in place.

Usage: patch-template-v13.py AGENT.yaml
v0.3.0 replaces the tools read_feed, read_topic, search and profile_posts with
find_posts, takes reply's target (a p ref) in place of target_url, target_id
and target_username, and post's trend in place of trend_ref. This rewrites the
persona sentences that name the old tools and arguments, writes AGENT.yaml.bak
first, prints every change and touches nothing else (dry_run included). A
second run changes nothing. A line that still names a removed tool or argument
after the rewrite is printed as a warning: edit it by hand.
"""
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
REMOVED = re.compile(r"\b(read_feed|read_topic|profile_posts|target_url|target_id|target_username|trend_ref)\b|\bsearch\b(?=[ ,.)])")


def patch(text):
    log = []
    for old, new in REWRITES:
        if old in text:
            text = text.replace(old, new)
            log.append("rewrote: " + old[:60] + "...")
    for number, line in enumerate(text.splitlines(), 1):
        if REMOVED.search(line):
            log.append("WARNING line %d still names a removed tool or argument, edit it by hand: %s..." % (number, line.strip()[:70]))
    return text, log


def main():
    path = sys.argv[1]
    with open(path, encoding="utf-8", newline="") as f:
        text = f.read()
    patched, log = patch(text)
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
