(() => {
  const REPO = "gcanosa/botIAask";
  const $ = (s, r = document) => r.querySelector(s), $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const esc = s => s.replace(/[&<>]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]));

  // theme: explicit choice wins, otherwise the OS preference (handled in CSS)
  const root = document.documentElement;
  try { const t = localStorage.getItem("theme"); if (t) root.dataset.theme = t; } catch (e) {}
  const isDark = () => root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
  const tbtn = $("#theme");
  const paint = () => { if (tbtn) { tbtn.textContent = isDark() ? "☀" : "☾"; tbtn.setAttribute("aria-label", isDark() ? "Switch to light theme" : "Switch to dark theme"); } };
  paint();
  tbtn && tbtn.addEventListener("click", () => {
    root.dataset.theme = isDark() ? "light" : "dark";
    try { localStorage.setItem("theme", root.dataset.theme); } catch (e) {}
    paint();
  });

  // mobile nav
  const nav = $(".nav");
  $("#burger") && $("#burger").addEventListener("click", () => nav.classList.toggle("open"));

  // copy buttons on every .term block
  $$(".term").forEach(t => {
    const b = document.createElement("button");
    b.className = "copy"; b.textContent = "copy"; b.type = "button";
    b.onclick = async () => {
      try { await navigator.clipboard.writeText($("pre", t).innerText.replace(/^\$ /gm, "")); b.textContent = "copied ✓"; }
      catch (e) { b.textContent = "press ⌘/Ctrl+C"; }
      setTimeout(() => (b.textContent = "copy"), 1600);
    };
    t.appendChild(b);
  });

  // tabs
  $$("[data-tabs]").forEach(g => {
    const tabs = $$(".tab", g), panels = $$(".panel", g);
    tabs.forEach((t, i) => t.addEventListener("click", () => {
      tabs.forEach((x, j) => { x.setAttribute("aria-selected", i === j); panels[j].hidden = i !== j; });
    }));
  });

  // docs scroll-spy
  const links = $$(".side a[href^='#']");
  if (links.length && "IntersectionObserver" in window) {
    const map = new Map(links.map(a => [a.getAttribute("href").slice(1), a]));
    const io = new IntersectionObserver(es => es.forEach(e => {
      if (e.isIntersecting) { links.forEach(l => l.classList.remove("on")); map.get(e.target.id)?.classList.add("on"); }
    }), { rootMargin: "-90px 0px -70% 0px" });
    map.forEach((_, id) => { const el = document.getElementById(id); el && io.observe(el); });
  }

  // reveal on scroll
  if ("IntersectionObserver" in window) {
    const io = new IntersectionObserver(es => es.forEach(e => e.isIntersecting && (e.target.classList.add("in"), io.unobserve(e.target))), { threshold: .12 });
    $$(".reveal").forEach(el => io.observe(el));
  } else $$(".reveal").forEach(el => el.classList.add("in"));

  // config template viewer: fetch, lightly highlight, download/copy
  const cfg = $("#cfg");
  if (cfg) fetch("config.yaml.template").then(r => r.text()).then(txt => {
    cfg.innerHTML = txt.split("\n").map(l => {
      const m = l.match(/^(\s*(?:#\s*)?)(- )?([\w.-]+)(:)(.*)$/);
      if (/^\s*#/.test(l) && !m) return `<span class="c">${esc(l)}</span>`;
      if (/^\s*#/.test(l)) return `<span class="c">${esc(l)}</span>`;
      if (m) return `${esc(m[1])}${m[2] || ""}<span class="k">${esc(m[3])}</span>:${/^\s*(true|false|\d+)\s*$/.test(m[5]) ? `<span class="n">${esc(m[5])}</span>` : `<span class="s">${esc(m[5])}</span>`}`;
      return esc(l);
    }).join("\n");
    cfg.dataset.raw = txt;
  }).catch(() => (cfg.textContent = "Could not load the template; get it from GitHub: config/config.yaml.template"));

  // releases from the GitHub API, falling back to tags, then to the static list already in the page
  const rel = $("#releases");
  if (rel) {
    const card = t => `<div class="card relcard"><div><h3 style="margin:0">${esc(t.name)}</h3><p style="margin:0;color:var(--muted)">${t.date ? new Date(t.date).toLocaleDateString(undefined, { dateStyle: "long" }) : ""}</p></div><a class="btn" href="${t.url}" rel="noopener">View on GitHub →</a></div>`;
    const j = u => fetch("https://api.github.com/repos/" + REPO + u, { headers: { Accept: "application/vnd.github+json" } }).then(r => { if (!r.ok) throw 0; return r.json(); });
    j("/releases?per_page=8").then(r => { if (!r.length) throw 0; return r.map(x => ({ name: x.name || x.tag_name, date: x.published_at, url: x.html_url })); })
      .catch(() => j("/tags?per_page=8").then(r => r.map(x => ({ name: x.name, url: `https://github.com/${REPO}/releases/tag/${x.name}` }))))
      .then(items => { rel.innerHTML = items.slice(0, +rel.dataset.n || 8).map(card).join(""); $("#relnote") && ($("#relnote").hidden = false); })
      .catch(() => {});
  }
})();
