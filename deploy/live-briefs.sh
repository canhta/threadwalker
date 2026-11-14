#!/bin/sh
# Build live-briefs.json from the last N wakes of an agent that wrote through the
# writer and critic. Run it on the host, against the host database. It reads the
# database only and calls no model. Text of other people's posts is removed
# unless KEEP_TEXT=1 (the parent and top-liked text are then in the file).
#
#   deploy/live-briefs.sh AGENT [N=10] [OUT=live-briefs.json]
#
# Needs jq and threadz on PATH (or THREADZ=/path/to/threadz). THREADZ_DB and
# THREADZ_AGENTS select the database and template directory as for any ctl command.
set -eu
agent=${1:?usage: live-briefs.sh AGENT [N] [OUT]}
want=${2:-10}
out=${3:-live-briefs.json}
bin=${THREADZ:-threadz}
keep=
[ "${KEEP_TEXT:-}" = 1 ] && keep=--keep-text
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
n=0
for id in $("$bin" ctl wakes --agent "$agent" --since 168h --limit 500 --json | jq -r '.[].id'); do
	[ "$n" -ge "$want" ] && break
	# A wake that wrote nothing through the writer has no brief; skip it.
	if "$bin" ctl wake-export "$id" --agent "$agent" --briefs $keep >"$tmp/$id.json" 2>/dev/null; then
		n=$((n + 1))
	fi
done
[ "$n" -gt 0 ] || { echo "no wake in the last 7 days recorded a writer exchange" >&2; exit 1; }
jq -s '{description: "Briefs from live wakes; live only", cases: map(.cases[])}' "$tmp"/*.json >"$out"
echo "wrote $n wakes to $out; run with --cases $(jq -r '[.cases[].id] | join(",")' "$out")"
