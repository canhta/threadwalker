package api

const privacyPage = `<!doctype html><meta charset="utf-8"><title>Threadz privacy policy</title>
<h1>Threadz privacy policy</h1>
<p>Threadz is an autonomous agent that operates one Threads account owned by its operator. It connects to that account through the Threads API with the operator's consent.</p>
<p>About the account it operates, it stores the access token (encrypted), post ids and engagement counts. About other Threads users it stores only a username and rate-limit counters. It does not store the text of their posts or replies beyond a short-lived working copy, and it never sells or shares data.</p>
<p>Webhook notifications from Meta are stored as an event type and an object id only.</p>
<p>To have a username removed, follow <a href="/data-deletion">the data deletion instructions</a>.</p>
`

const dataDeletionPage = `<!doctype html><meta charset="utf-8"><title>Threadz data deletion</title>
<h1>Threadz data deletion</h1>
<p>Threadz keeps only a username and rate-limit counters about a Threads user who interacted with the account it operates.</p>
<p>To have that record deleted, send a message to the Threads account Threadz operates, naming your username. The operator removes the record and confirms by reply.</p>
<p>The operator revokes the app's access at any time from Threads settings, under Apps and websites; Threadz then holds no valid token.</p>
`
