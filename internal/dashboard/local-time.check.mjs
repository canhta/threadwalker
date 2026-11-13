// Run: TZ=Asia/Ho_Chi_Minh node internal/dashboard/local-time.check.mjs
import assert from "node:assert";
import { createRequire } from "node:module";
const lt = createRequire(import.meta.url)("./static/local-time.js").localTime;
const d = new Date("2026-11-02T20:05:00Z"), now = new Date("2026-11-12T12:00:00Z");
assert.equal(lt.format("day", d, now), "3 Nov");
assert.equal(lt.format("clock", d, now), "3 Nov 03:05");
assert.equal(lt.format("clock", new Date("2026-11-12T10:05:00Z"), now), "17:05");
assert.equal(lt.format("full", d, now), "3 Nov 2026, 03:05");
assert.equal(lt.format("stamp", d, now), "3 Nov 03:05");
console.log("ok");
