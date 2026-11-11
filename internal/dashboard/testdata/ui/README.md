# Dashboard UI evidence (synthetic only)

These captures come from the actual compiled dashboard server (`http://127.0.0.1:18081/`)
serving the template and CSS at commit `93fca50`, against a temporary SQLite database
seeded with two secret-free synthetic templates. No real Threads account, Telegram bot,
provider API, VPS or operator `.env` was used.

| Width | Screenshot | Layout measurements | Rendered page |
|---|---|---|---|
| 1280px desktop | `dashboard-1280.png` (1280x1431) | `layout-1280.json` | full page |
| 390px phone | `dashboard-390.png` (390x3664) | `layout-390.json` | full page |
| 320px phone | `dashboard-320.png` (320x3657) | `layout-320.json` | full page |

The Orca embedded browser rendered each width with the viewport height set to the page's
rendered scroll height, so one screenshot covers the whole page without stitching. For each
width the JSON records `scrollWidth == clientWidth`, `horizontal_overflow: 0`, no visible
element whose `scrollWidth` exceeds its `clientWidth`, and equal table client/scroll widths.
The page was inspected at each size; the only visual defect found was the desktop next-wake
timestamp breaking at hyphens, fixed in the committed CSS by making agent figures full width.

The data is illustrative synthetic state: two agents, budgets, wakes, confirmed actions,
playbook entries and allowlisted tool details. The screenshots are not live engagement or
deployment evidence.
