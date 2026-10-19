// Shared design system: one stylesheet, one header, one set of components.
//
// The UI is server-rendered HTML with no JavaScript. The design follows
// Refactoring UI principles adapted to that constraint: a grey-first palette
// (colour only for semantic states), a tight type scale (page title, section,
// body, meta), generous whitespace over borders, and hierarchy through size
// and weight rather than decoration. Every page shares this stylesheet so
// the three flows (search, decide, review) read as one tool.
package web

const sharedCSS = `
:root {
  --ink: #1a1a1a;
  --ink-soft: #444;
  --muted: #6b6b6b;
  --faint: #999;
  --line: #e4e4e4;
  --line-soft: #f0f0f0;
  --bg: #fff;
  --bg-wash: #fafafa;
  --accent: #1d4ed8;
  --accent-ink: #1e40af;
  --accent-wash: #eff4ff;
  --good: #166534;
  --good-wash: #f0fdf4;
  --good-line: #bbf7d0;
  --bad: #b91c1c;
  --bad-wash: #fef2f2;
  --bad-line: #fecaca;
  --warn-bg: #fffbeb;
  --warn-line: #fcd34d;
  --warn-ink: #92400e;
}
@media (prefers-color-scheme: dark) {
  :root {
    --ink: #e8e8e8;
    --ink-soft: #c9c9c9;
    --muted: #a3a3a3;
    --faint: #737373;
    --line: #333;
    --line-soft: #262626;
    --bg: #141414;
    --bg-wash: #1d1d1d;
    --accent: #7aa5ff;
    --accent-ink: #a9c2ff;
    --accent-wash: #1c2742;
    --good: #7ddba3;
    --good-wash: #14261b;
    --good-line: #24573c;
    --bad: #ff9d9d;
    --bad-wash: #2b1616;
    --bad-line: #5c2626;
    --warn-bg: #2a2210;
    --warn-line: #6b5416;
    --warn-ink: #f0cf7d;
  }
}
* { box-sizing: border-box; }
body {
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
  margin: 0; padding: 0 1rem 4rem; color: var(--ink);
  background: var(--bg); line-height: 1.5;
}
.page { max-width: 880px; margin: 0 auto; }
.topbar {
  display: flex; align-items: baseline; gap: 1rem;
  padding: 1.25rem 0 .75rem; border-bottom: 1px solid var(--line);
  margin-bottom: 1.5rem;
}
.topbar .brand { font-size: 1.1rem; font-weight: 700; color: var(--ink); text-decoration: none; }
.topbar nav { font-size: .9rem; }
.topbar nav a { color: var(--muted); text-decoration: none; margin-right: 1rem; }
.topbar nav a:hover { color: var(--accent-ink); text-decoration: underline; }
h1 { font-size: 1.35rem; margin: 0 0 .25rem; font-weight: 700; }
h2 { font-size: 1.05rem; margin: 1.75rem 0 .75rem; font-weight: 650; }
.sub { color: var(--muted); font-size: .92rem; margin: 0 0 1rem; }
.meta { color: var(--muted); font-size: .85rem; }
.card {
  border: 1px solid var(--line); border-radius: 10px;
  padding: 1rem 1.15rem; margin-bottom: .8rem; background: var(--bg);
}
.card.tone-accept { border-color: var(--good-line); background: var(--good-wash); }
.card.tone-reject { border-color: var(--bad-line); background: var(--bad-wash); }
.card .title { font-weight: 650; font-size: 1rem; }
.card .byline { color: var(--muted); font-size: .88rem; margin-top: .1rem; }
.pill {
  display: inline-block; font-size: .75rem; font-weight: 600;
  padding: .1rem .55rem; border-radius: 4px;
  background: var(--bg-wash); border: 1px solid var(--line); color: var(--ink-soft);
  vertical-align: middle;
}
.pill.state-confirm, .pill.state-tag-review { background: var(--accent-wash); border-color: #bfdbfe; color: var(--accent-ink); }
.pill.state-done { background: var(--good-wash); border-color: var(--good-line); color: var(--good); }
.pill.state-failed { background: var(--bad-wash); border-color: var(--bad-line); color: var(--bad); }
.pill.band-accept { background: var(--good-wash); border-color: var(--good-line); color: var(--good); }
.pill.band-reject { background: var(--bad-wash); border-color: var(--bad-line); color: var(--bad); }
.reasons { margin: .5rem 0 0; padding: 0; list-style: none; font-size: .83rem; }
.reasons li { display: inline-block; margin: 0 .8rem .2rem 0; }
.reasons li.evidence-extra { display: none; }
.card.show-all .reasons li.evidence-extra { display: inline-block; }
.evidence-toggle { font-size: .78rem; color: var(--faint); background: none; border: 0; padding: 0; cursor: pointer; text-decoration: underline; }
.hit { color: var(--good); }
.miss { color: var(--bad); }
.note { color: var(--muted); }
.banner {
  border-radius: 8px; padding: .65rem .9rem; margin-bottom: 1.2rem; font-size: .88rem;
}
.banner.warn { background: var(--warn-bg); border: 1px solid var(--warn-line); color: var(--warn-ink); }
.banner.bad { background: var(--bad-wash); border: 1px solid var(--bad-line); color: var(--bad); }
.banner.good { background: var(--good-wash); border: 1px solid var(--good-line); color: var(--good); }
form.search { display: flex; gap: .5rem; margin: 0 0 1.75rem; }
form.search input[type=search] {
  flex: 1; font-size: 1rem; padding: .6rem .8rem;
  border: 1px solid var(--line); border-radius: 8px;
}
form.search input[type=search]:focus { outline: 2px solid var(--accent); border-color: var(--accent); }
.btn {
  font-size: .88rem; font-weight: 550; padding: .45rem 1rem; cursor: pointer;
  border-radius: 8px; border: 1px solid var(--line); background: var(--bg);
  color: var(--ink); text-decoration: none; display: inline-block;
}
.btn:hover { border-color: var(--faint); background: var(--bg-wash); }
.btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
.btn.primary:hover { background: var(--accent-ink); }
.btn.danger { color: var(--bad); border-color: var(--bad-line); }
.btn.danger:hover { background: var(--bad-wash); }
form.inline { display: inline-block; margin: 0; }
.files { margin: .5rem 0; padding: 0; list-style: none; font-size: .82rem; }
.files li { padding: .2rem 0; border-top: 1px dotted var(--line); }
.files label { display: flex; gap: .5rem; align-items: baseline; cursor: pointer; }
.files code { font-family: ui-monospace, monospace; word-break: break-all; }
table.jobs { width: 100%; border-collapse: collapse; font-size: .92rem; }
table.jobs th { text-align: left; font-size: .78rem; text-transform: uppercase; letter-spacing: .04em; color: var(--faint); padding: .4rem .5rem; border-bottom: 1px solid var(--line); }
table.jobs td { padding: .55rem .5rem; border-bottom: 1px solid var(--line-soft); }
table.jobs tr:hover td { background: var(--bg-wash); }
table.jobs a.rowlink { color: var(--ink); font-weight: 550; text-decoration: none; }
table.jobs a.rowlink:hover { color: var(--accent-ink); text-decoration: underline; }
.bar { background: var(--line-soft); border-radius: 4px; height: 8px; min-width: 80px; overflow: hidden; }
.bar > div { background: var(--accent); height: 8px; }
.field { margin: 0 0 .8rem; }
.field label { display: block; font-size: .85rem; font-weight: 600; color: var(--ink-soft); margin-bottom: .2rem; }
.field input, .field textarea {
  display: block; width: 100%; max-width: 620px; font-size: .92rem;
  padding: .5rem .65rem; border: 1px solid var(--line); border-radius: 8px;
}
.field input:focus, .field textarea:focus { outline: 2px solid var(--accent); border-color: var(--accent); }
.field textarea { min-height: 5rem; resize: vertical; }
.field .hint { font-size: .8rem; color: var(--faint); margin-top: .15rem; }
.actions { margin-top: 1.2rem; display: flex; gap: .6rem; align-items: center; }
.release { display: flex; justify-content: space-between; align-items: baseline; gap: 1rem; padding: .5rem 0; border-top: 1px dotted var(--line); font-size: .9rem; }
.release:first-of-type { border-top: 0; }
.release .id { font-family: ui-monospace, monospace; font-size: .75rem; color: var(--faint); }
.empty { color: var(--faint); font-style: italic; }
@media (max-width: 640px) {
  .topbar { flex-wrap: wrap; }
  form.search { flex-direction: column; }
  .release { flex-direction: column; align-items: stretch; gap: .4rem; }
}
`
