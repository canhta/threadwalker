# Synthetic trend fixtures

These files are hand-written synthetic responses for offline `fetch_trends`
tests. They are not captured from a live service and contain no account,
credential or personal data.

Shapes follow the public Hacker News API (`topstories.json` and `item/<id>.json`)
and the documented RSS/Atom feed formats. Titles deliberately include XML
entities, CDATA, Vietnamese/Japanese text, missing fields and a dead item so the
tests exercise decoding, skipping and Unicode handling without network access.
