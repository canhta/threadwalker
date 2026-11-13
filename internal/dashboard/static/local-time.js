// Rewrites each <time datetime> in the viewer's timezone. The server text is
// the fallback when this script does not run.
(function (root) {
  var o = {};
  function p(d, opt) {
    var m = {};
    new Intl.DateTimeFormat("en-GB", Object.assign({ hourCycle: "h23" }, opt)).formatToParts(d).forEach(function (x) { m[x.type] = x.value; });
    return m;
  }
  var hm = { hour: "2-digit", minute: "2-digit" };
  var dm = { day: "numeric", month: "short" };
  function clock(d) { var t = p(d, hm); return t.hour + ":" + t.minute; }
  function day(d) { var t = p(d, dm); return t.day + " " + t.month.slice(0, 3); }
  function key(d) { var t = p(d, { year: "numeric", month: "numeric", day: "numeric" }); return t.year + "-" + t.month + "-" + t.day; }
  o.format = function (kind, d, now) {
    switch (kind) {
      case "clock": return key(d) === key(now) ? clock(d) : day(d) + " " + clock(d);
      case "day": return day(d);
      case "stamp": return day(d) + " " + clock(d);
      case "full": return day(d) + " " + p(d, { year: "numeric" }).year + ", " + clock(d);
    }
  };
  o.title = function (el) {
    var d = new Date(el.getAttribute("datetime")), t = o.format("full", d, d);
    var end = el.getAttribute("data-end");
    return end ? t + " to " + clock(new Date(end)) : t;
  };
  o.run = function (doc) {
    var now = new Date();
    doc.querySelectorAll("time[datetime]").forEach(function (el) {
      var d = new Date(el.getAttribute("datetime"));
      if (isNaN(d)) return;
      var kind = el.getAttribute("data-fmt");
      if (kind) el.textContent = o.format(kind, d, now);
      if (el.hasAttribute("data-title")) el.title = o.title(el);
    });
  };
  if (root.document) o.run(root.document); else root.localTime = o;
})(typeof window !== "undefined" ? window : module.exports = {});
