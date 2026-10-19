// Install tabs.
const tabs = [...document.querySelectorAll('[role="tab"]')];

function selectTab(tab) {
  for (const t of tabs) {
    const on = t === tab;
    t.setAttribute("aria-selected", on);
    t.tabIndex = on ? 0 : -1;
    document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
  }
}

tabs.forEach((tab, i) => {
  tab.addEventListener("click", () => selectTab(tab));
  tab.addEventListener("keydown", (e) => {
    const step = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
    if (!step) return;
    const next = tabs[(i + step + tabs.length) % tabs.length];
    selectTab(next);
    next.focus();
  });
});

for (const button of document.querySelectorAll(".copy")) {
  button.addEventListener("click", async () => {
    const text = button.parentElement.querySelector("code").textContent;
    try {
      await navigator.clipboard.writeText(text);
      button.textContent = "Copied";
    } catch {
      button.textContent = "Copy failed";
    }
    setTimeout(() => (button.textContent = "Copy"), 1500);
  });
}

// Hero diagram: many client lines merge into pgmux, and a few server lines go to Postgres.
const svg = document.getElementById("flow");
const NS = "http://www.w3.org/2000/svg";

function el(name, attrs, parent = svg) {
  const node = document.createElementNS(NS, name);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  parent.appendChild(node);
  return node;
}

function dot(path, cls, dur, begin) {
  const c = el("circle", { r: cls === "server-dot" ? 3.5 : 2.6, class: cls });
  const motion = el("animateMotion", { dur: `${dur}s`, begin: `${begin}s`, repeatCount: "indefinite", path }, c);
  motion.setAttribute("rotate", "0");
}

if (svg) {
  const clients = 15;
  const boxX = 186, boxY = 140, boxW = 148, boxH = 100, midY = boxY + boxH / 2;

  for (let i = 0; i < clients; i++) {
    const y = 14 + i * (352 / (clients - 1));
    const endY = midY - 30 + (i / (clients - 1)) * 60;
    const path = `M 20 ${y} C 110 ${y}, 120 ${endY}, ${boxX} ${endY}`;
    el("path", { d: path, class: "client-line" });
    el("circle", { cx: 16, cy: y, r: 4, class: "client-node" });
    dot(path, "client-dot", 2.6, -(i * 0.37) % 2.6);
  }

  const servers = [midY - 24, midY, midY + 24];
  servers.forEach((y, i) => {
    const path = `M ${boxX + boxW} ${y} L 430 ${y}`;
    el("path", { d: path, class: "server-line" });
    for (let k = 0; k < 3; k++) dot(path, "server-dot", 1.1, -(k * 0.37 + i * 0.12));
  });

  el("rect", { x: boxX, y: boxY, width: boxW, height: boxH, rx: 12, class: "box" });
  el("text", { x: boxX + boxW / 2, y: midY + 2, "text-anchor": "middle", class: "box-label" }).textContent = "pgmux";
  el("text", { x: boxX + boxW / 2, y: midY + 22, "text-anchor": "middle", class: "box-sub" }).textContent = "transaction mode";

  // Postgres as a cylinder.
  const dbX = 430, dbW = 76, dbTop = midY - 46, dbH = 92, ry = 10;
  el("path", {
    d: `M ${dbX} ${dbTop} v ${dbH} a ${dbW / 2} ${ry} 0 0 0 ${dbW} 0 v ${-dbH}`,
    class: "db",
  });
  el("ellipse", { cx: dbX + dbW / 2, cy: dbTop, rx: dbW / 2, ry, class: "db" });
  el("text", { x: dbX + dbW / 2, y: midY + 12, "text-anchor": "middle", class: "db-label" }).textContent = "Postgres";

  const reduce = window.matchMedia("(prefers-reduced-motion: reduce)");
  const sync = () => (reduce.matches ? svg.pauseAnimations() : svg.unpauseAnimations());
  sync();
  reduce.addEventListener("change", sync);
}
