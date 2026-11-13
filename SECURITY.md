# Security policy

Only the latest release gets security fixes.

## Report a vulnerability

Open a private advisory at <https://github.com/canhta/threadz/security/advisories/new> with the
problem, the affected version or commit, and the smallest reproduction you can share. Do not open a
public issue. Leave out credentials, session cookies and private Threads content, and redact logs.

The maintainer replies within about seven days and credits the reporter in the advisory unless asked
not to.

## In scope

- Credential handling: keys from environment variables, `.env` loading, redaction in logs and the
  model context, the dashboard token.
- The dashboard: content-mode authentication, cache privacy, the loopback-only listener, rate
  limiting, proxy handling and request parsing.
- The guards: budget and action caps, forbidden phrases, do-not-contact entries, own-account
  protection, audit rows, action reservations and the unknown-outcome path.
- The template, catalog and CLI parsers, where a malformed file could bypass a limit or expose a
  secret.
- The Threads API client, the OAuth and webhook server (`run --api-listen`), the admin socket and the
  feed browser adapter, where crafted input could make the agent act outside its allowed tools.

## Out of scope

- Meta limiting or removing an account that posts automatically.
- A model writing a bad post within the limits the operator set.
- Bugs in third-party dependencies; report those upstream.
