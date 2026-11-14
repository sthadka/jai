// Self-contained flow-metrics dashboard renderer.
//
// Pure vanilla JS + inline SVG, no external library, no network, no fetch. All data comes
// from the embedded <script id="dashboard-data"> JSON block and is inserted into the DOM via
// textContent / SVG text nodes ONLY — never innerHTML — so any Jira-controlled string
// (issue summary, sprint name, assignee) is rendered as inert text and cannot inject markup
// or script.
(function () {
  "use strict";

  var SVGNS = "http://www.w3.org/2000/svg";
  var PALETTE = ["--c0", "--c1", "--c2", "--c3", "--c4", "--c5", "--c6"];

  function color(i) { return "var(" + PALETTE[i % PALETTE.length] + ")"; }

  // el builds an HTML element; text is set via textContent (safe for hostile strings).
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = String(text);
    return n;
  }

  // s builds an SVG element and sets attributes; text content via textContent.
  function s(tag, attrs, text) {
    var n = document.createElementNS(SVGNS, tag);
    if (attrs) for (var k in attrs) if (attrs.hasOwnProperty(k)) n.setAttribute(k, attrs[k]);
    if (text != null) n.textContent = String(text);
    return n;
  }

  function svgRoot(w, h) {
    return s("svg", { viewBox: "0 0 " + w + " " + h, width: w, height: h,
      preserveAspectRatio: "xMinYMin meet", role: "img" });
  }

  function niceMax(v) {
    if (v <= 0) return 1;
    var mag = Math.pow(10, Math.floor(Math.log10(v)));
    var n = v / mag;
    var step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
    return step * mag;
  }

  function legend(entries) {
    var box = el("div", "legend");
    entries.forEach(function (e, i) {
      var key = el("div", "key");
      var sw = el("span", "swatch");
      sw.style.background = e.color || color(i);
      key.appendChild(sw);
      key.appendChild(el("span", null, e.name));
      box.appendChild(key);
    });
    return box;
  }

  // ---- Chart renderers. Each returns a DOM node (or null => fall back to a message). ----

  // Box / whisker from a 5-number-ish distribution summary (min, p50, p85, p95, max).
  function renderBox(data) {
    var W = 520, H = 160, padL = 48, padR = 16, padT = 20, padB = 42;
    var innerW = W - padL - padR, innerH = H - padT - padB;
    var max = niceMax(data.max > 0 ? data.max : 1);
    var x = function (v) { return padL + (v / max) * innerW; };
    var svg = svgRoot(W, H);
    // axis
    svg.appendChild(s("line", { class: "axis", x1: padL, y1: padT + innerH, x2: padL + innerW, y2: padT + innerH, stroke: "currentColor" }));
    var ticks = 5, cy = padT + innerH / 2, bh = 34;
    for (var i = 0; i <= ticks; i++) {
      var tv = (max / ticks) * i, tx = x(tv);
      svg.appendChild(s("line", { class: "gridline", x1: tx, y1: padT, x2: tx, y2: padT + innerH }));
      svg.appendChild(s("text", { x: tx, y: padT + innerH + 16, "text-anchor": "middle" }, round(tv)));
    }
    // whisker min..max
    svg.appendChild(s("line", { x1: x(data.min), y1: cy, x2: x(data.max), y2: cy, stroke: color(0), "stroke-width": 2 }));
    // box p50..p95
    svg.appendChild(s("rect", { x: x(data.p50), y: cy - bh / 2, width: Math.max(1, x(data.p95) - x(data.p50)), height: bh, fill: color(0), "fill-opacity": 0.35, stroke: color(0) }));
    // markers
    [["p50", data.p50], ["p85", data.p85], ["p95", data.p95]].forEach(function (m) {
      svg.appendChild(s("line", { x1: x(m[1]), y1: cy - bh / 2 - 4, x2: x(m[1]), y2: cy + bh / 2 + 4, stroke: color(2), "stroke-width": 1.5 }));
      svg.appendChild(s("text", { x: x(m[1]), y: cy - bh / 2 - 8, "text-anchor": "middle" }, m[0]));
    });
    // caption
    svg.appendChild(s("text", { x: padL, y: H - 6 },
      "n=" + data.count + "  p50=" + round(data.p50) + "  p85=" + round(data.p85) +
      "  p95=" + round(data.p95) + "  mean=" + round(data.mean) + " " + (data.unit || "")));
    return svg;
  }

  // Percentile lines over periods (p50/p85/p95). Values may contain nulls (gaps).
  function renderPlines(data) {
    var series = data.series || [], periods = data.periods || [];
    var W = 560, H = 240, padL = 48, padR = 16, padT = 16, padB = 54;
    var innerW = W - padL - padR, innerH = H - padT - padB;
    var max = 0;
    series.forEach(function (ser) { (ser.values || []).forEach(function (v) { if (v != null && v > max) max = v; }); });
    max = niceMax(max || 1);
    var n = periods.length;
    var x = function (i) { return n <= 1 ? padL + innerW / 2 : padL + (i / (n - 1)) * innerW; };
    var y = function (v) { return padT + innerH - (v / max) * innerH; };
    var svg = svgRoot(W, H);
    var ticks = 5;
    for (var t = 0; t <= ticks; t++) {
      var tv = (max / ticks) * t, ty = y(tv);
      svg.appendChild(s("line", { class: "gridline", x1: padL, y1: ty, x2: padL + innerW, y2: ty }));
      svg.appendChild(s("text", { x: padL - 6, y: ty + 3, "text-anchor": "end" }, round(tv)));
    }
    periods.forEach(function (p, i) {
      if (n <= 12 || i % Math.ceil(n / 12) === 0) {
        svg.appendChild(s("text", { x: x(i), y: padT + innerH + 16, "text-anchor": "middle" }, p));
      }
    });
    series.forEach(function (ser, si) {
      var pts = [], vals = ser.values || [];
      vals.forEach(function (v, i) {
        if (v == null) { if (pts.length) { drawPoly(svg, pts, color(si)); pts = []; } return; }
        pts.push([x(i), y(v)]);
        svg.appendChild(s("circle", { cx: x(i), cy: y(v), r: 2.5, fill: color(si) }));
      });
      if (pts.length) drawPoly(svg, pts, color(si));
    });
    var wrap = el("div");
    wrap.appendChild(svg);
    wrap.appendChild(legend(series.map(function (ser, i) { return { name: ser.name, color: color(i) }; })));
    return wrap;
  }

  function drawPoly(svg, pts, col) {
    if (pts.length === 1) { svg.appendChild(s("circle", { cx: pts[0][0], cy: pts[0][1], r: 3, fill: col })); return; }
    svg.appendChild(s("polyline", { points: pts.map(function (p) { return p[0] + "," + p[1]; }).join(" "),
      fill: "none", stroke: col, "stroke-width": 2 }));
  }

  // Cumulative flow diagram: stacked area across all stages.
  function renderCFD(data) {
    var dates = data.dates || [], series = data.series || [];
    var W = 560, H = 260, padL = 48, padR = 16, padT = 16, padB = 54;
    var innerW = W - padL - padR, innerH = H - padT - padB;
    var n = dates.length;
    var totals = new Array(n).fill(0);
    series.forEach(function (ser) { (ser.values || []).forEach(function (v, i) { totals[i] += v; }); });
    var max = niceMax(Math.max.apply(null, totals.concat([1])));
    var x = function (i) { return n <= 1 ? padL + innerW / 2 : padL + (i / (n - 1)) * innerW; };
    var y = function (v) { return padT + innerH - (v / max) * innerH; };
    var svg = svgRoot(W, H);
    var ticks = 5;
    for (var t = 0; t <= ticks; t++) {
      var tv = (max / ticks) * t, ty = y(tv);
      svg.appendChild(s("line", { class: "gridline", x1: padL, y1: ty, x2: padL + innerW, y2: ty }));
      svg.appendChild(s("text", { x: padL - 6, y: ty + 3, "text-anchor": "end" }, round(tv)));
    }
    var cum = new Array(n).fill(0);
    series.forEach(function (ser, si) {
      var lower = cum.slice();
      var upper = ser.values.map(function (v, i) { return cum[i] + v; });
      var pts = [];
      for (var i = 0; i < n; i++) pts.push(x(i) + "," + y(upper[i]));
      for (var j = n - 1; j >= 0; j--) pts.push(x(j) + "," + y(lower[j]));
      svg.appendChild(s("polygon", { points: pts.join(" "), fill: color(si), "fill-opacity": 0.75, stroke: color(si), "stroke-width": 0.5 }));
      cum = upper;
    });
    dates.forEach(function (d, i) {
      if (n <= 10 || i % Math.ceil(n / 10) === 0 || i === n - 1) {
        svg.appendChild(s("text", { x: x(i), y: padT + innerH + 16, "text-anchor": "middle" }, d));
      }
    });
    var wrap = el("div");
    wrap.appendChild(svg);
    wrap.appendChild(legend(series.map(function (ser, i) { return { name: ser.name, color: color(i) }; })));
    return wrap;
  }

  // Vertical bars (throughput, buckets).
  function renderBars(data) {
    var labels = data.labels || [], values = data.values || [];
    var n = labels.length;
    var W = Math.max(360, Math.min(720, 60 + n * 46)), H = 240, padL = 44, padR = 12, padT = 16, padB = 60;
    var innerW = W - padL - padR, innerH = H - padT - padB;
    var max = niceMax(Math.max.apply(null, values.concat([1])));
    var bw = innerW / n * 0.68, gap = innerW / n;
    var y = function (v) { return padT + innerH - (v / max) * innerH; };
    var svg = svgRoot(W, H);
    svg.appendChild(s("line", { class: "axis", x1: padL, y1: padT + innerH, x2: padL + innerW, y2: padT + innerH, stroke: "currentColor" }));
    var ticks = 5;
    for (var t = 0; t <= ticks; t++) {
      var tv = (max / ticks) * t, ty = y(tv);
      svg.appendChild(s("line", { class: "gridline", x1: padL, y1: ty, x2: padL + innerW, y2: ty }));
      svg.appendChild(s("text", { x: padL - 6, y: ty + 3, "text-anchor": "end" }, round(tv)));
    }
    labels.forEach(function (lab, i) {
      var cx = padL + gap * i + (gap - bw) / 2;
      var v = values[i] || 0;
      svg.appendChild(s("rect", { x: cx, y: y(v), width: bw, height: padT + innerH - y(v), fill: color(0), rx: 2 }));
      svg.appendChild(s("text", { x: cx + bw / 2, y: y(v) - 4, "text-anchor": "middle" }, v));
      var tl = s("text", { x: cx + bw / 2, y: padT + innerH + 14, "text-anchor": "end",
        transform: "rotate(-40 " + (cx + bw / 2) + " " + (padT + innerH + 14) + ")" }, lab);
      svg.appendChild(tl);
    });
    return svg;
  }

  // Grouped bars (sprint ever-members vs resolved).
  function renderGrouped(data) {
    var labels = data.labels || [], series = data.series || [];
    var n = labels.length, g = series.length;
    var W = Math.max(380, Math.min(760, 70 + n * (g * 18 + 20))), H = 250, padL = 44, padR = 12, padT = 16, padB = 70;
    var innerW = W - padL - padR, innerH = H - padT - padB;
    var max = 1;
    series.forEach(function (ser) { ser.values.forEach(function (v) { if (v > max) max = v; }); });
    max = niceMax(max);
    var slot = innerW / n, bw = Math.min(16, (slot * 0.8) / g);
    var y = function (v) { return padT + innerH - (v / max) * innerH; };
    var svg = svgRoot(W, H);
    svg.appendChild(s("line", { class: "axis", x1: padL, y1: padT + innerH, x2: padL + innerW, y2: padT + innerH, stroke: "currentColor" }));
    var ticks = 5;
    for (var t = 0; t <= ticks; t++) {
      var tv = (max / ticks) * t, ty = y(tv);
      svg.appendChild(s("line", { class: "gridline", x1: padL, y1: ty, x2: padL + innerW, y2: ty }));
      svg.appendChild(s("text", { x: padL - 6, y: ty + 3, "text-anchor": "end" }, round(tv)));
    }
    labels.forEach(function (lab, i) {
      var base = padL + slot * i + (slot - bw * g) / 2;
      series.forEach(function (ser, si) {
        var v = ser.values[i] || 0, bx = base + si * bw;
        svg.appendChild(s("rect", { x: bx, y: y(v), width: bw - 1, height: padT + innerH - y(v), fill: color(si), rx: 1 }));
      });
      var cx = padL + slot * i + slot / 2;
      svg.appendChild(s("text", { x: cx, y: padT + innerH + 14, "text-anchor": "end",
        transform: "rotate(-40 " + cx + " " + (padT + innerH + 14) + ")" }, lab));
    });
    var wrap = el("div");
    wrap.appendChild(svg);
    wrap.appendChild(legend(series.map(function (ser, i) { return { name: ser.name, color: color(i) }; })));
    return wrap;
  }

  // Horizontal bars (workload).
  function renderHBars(data) {
    var labels = data.labels || [], values = data.values || [];
    var n = labels.length;
    var rowH = 26, W = 560, padL = 160, padR = 44, padT = 10, padB = 10;
    var H = padT + padB + n * rowH;
    var innerW = W - padL - padR;
    var max = niceMax(Math.max.apply(null, values.concat([1])));
    var x = function (v) { return (v / max) * innerW; };
    var svg = svgRoot(W, H);
    labels.forEach(function (lab, i) {
      var cy = padT + i * rowH;
      var v = values[i] || 0;
      svg.appendChild(s("rect", { x: padL, y: cy + 3, width: Math.max(1, x(v)), height: rowH - 8, fill: color(0), rx: 2 }));
      svg.appendChild(s("text", { x: padL - 8, y: cy + rowH / 2 + 2, "text-anchor": "end" }, lab));
      svg.appendChild(s("text", { x: padL + x(v) + 6, y: cy + rowH / 2 + 2 }, v));
    });
    return svg;
  }

  // Lines over periods (net flow), with a zero baseline.
  function renderLines(data) {
    var labels = data.labels || [], values = data.values || [];
    var W = 560, H = 230, padL = 48, padR = 16, padT = 16, padB = 54;
    var innerW = W - padL - padR, innerH = H - padT - padB;
    var lo = Math.min.apply(null, values.concat([0]));
    var hi = Math.max.apply(null, values.concat([0]));
    if (hi === lo) hi = lo + 1;
    var y = function (v) { return padT + innerH - ((v - lo) / (hi - lo)) * innerH; };
    var n = labels.length;
    var x = function (i) { return n <= 1 ? padL + innerW / 2 : padL + (i / (n - 1)) * innerW; };
    var svg = svgRoot(W, H);
    var zeroY = y(0);
    svg.appendChild(s("line", { class: "axis", x1: padL, y1: zeroY, x2: padL + innerW, y2: zeroY, stroke: "currentColor" }));
    svg.appendChild(s("text", { x: padL - 6, y: zeroY + 3, "text-anchor": "end" }, "0"));
    var pts = [];
    values.forEach(function (v, i) {
      pts.push([x(i), y(v)]);
      svg.appendChild(s("circle", { cx: x(i), cy: y(v), r: 2.5, fill: v < 0 ? color(3) : color(1) }));
    });
    drawPoly(svg, pts, color(0));
    labels.forEach(function (lab, i) {
      if (n <= 12 || i % Math.ceil(n / 12) === 0) {
        svg.appendChild(s("text", { x: x(i), y: padT + innerH + 16, "text-anchor": "middle" }, lab));
      }
    });
    return svg;
  }

  // Ratio card.
  function renderRatio(data) {
    var wrap = el("div");
    var val = data.value == null ? "undefined" : Number(data.value).toFixed(3);
    wrap.appendChild(el("div", "ratio-value", val));
    wrap.appendChild(el("div", "ratio-detail",
      data.numerator + " / " + data.denominator + " = " + data.numCount + " / " + data.denCount +
      (data.value == null ? "  (zero denominator)" : "")));
    if (data.classLabels && data.classLabels.length) {
      wrap.appendChild(renderBars({ labels: data.classLabels, values: data.classValues }));
    }
    return wrap;
  }

  var RENDERERS = {
    box: renderBox, plines: renderPlines, cfd: renderCFD, bars: renderBars,
    hbars: renderHBars, lines: renderLines, ratio: renderRatio, grouped: renderGrouped
  };

  function round(v) {
    if (v == null) return "";
    var r = Math.round(v * 100) / 100;
    return (Math.abs(r - Math.round(r)) < 1e-9) ? String(Math.round(r)) : String(r);
  }

  function renderSection(sec) {
    var card = el("div", "card" + (sec.available ? "" : " unavailable"));
    card.appendChild(el("h2", null, sec.title));
    if (sec.subtitle) card.appendChild(el("p", "subtitle", sec.subtitle));
    if (!sec.available) {
      card.appendChild(el("div", "unavailable-msg", sec.reason || "unavailable"));
    } else {
      var chart = el("div", "chart");
      var fn = RENDERERS[sec.chart];
      try {
        var node = fn ? fn(sec.data) : null;
        if (node) chart.appendChild(node);
        else chart.appendChild(el("div", "unavailable-msg", "unsupported chart: " + sec.chart));
      } catch (e) {
        chart.appendChild(el("div", "unavailable-msg", "render error: " + e.message));
      }
      card.appendChild(chart);
    }
    if (sec.notes && sec.notes.length) {
      var ul = el("ul", "notes");
      sec.notes.forEach(function (nt) {
        var li = el("li", /proxy|not applied|warn|exclud/i.test(nt) ? "warn" : null, nt);
        ul.appendChild(li);
      });
      card.appendChild(ul);
    }
    return card;
  }

  function init() {
    var raw = document.getElementById("dashboard-data").textContent;
    var d;
    try { d = JSON.parse(raw); } catch (e) {
      document.getElementById("dash-sections").appendChild(el("div", "unavailable-msg", "failed to parse embedded data"));
      return;
    }
    document.getElementById("dash-title").textContent = d.title || "Flow Metrics";
    document.title = (d.title || "Flow Metrics") + " — jai";

    var meta = document.getElementById("dash-meta");
    function pill(label) { var p = el("span", "pill", label); meta.appendChild(p); }
    pill("as of " + (d.asOf || ""));
    pill("projects: " + (d.projects && d.projects.length ? d.projects.join(", ") : "all"));
    if (d.range) pill("trend " + (d.range.from || "…") + " → " + (d.range.to || "…"));
    if (d.build) pill("generation " + d.build.generation + (d.build.builtAt ? " (built " + d.build.builtAt + ")" : ""));
    if (d.freshness) {
      Object.keys(d.freshness).forEach(function (k) {
        var f = d.freshness[k];
        if (f && f.lastSync) pill(k + " synced " + f.lastSync);
      });
    }
    pill("generated " + (d.generatedAt || ""));

    if (d.disclosure) document.getElementById("dash-disclosure").textContent = d.disclosure;

    var host = document.getElementById("dash-sections");
    (d.sections || []).forEach(function (sec) { host.appendChild(renderSection(sec)); });
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
