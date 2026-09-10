# 12. Vanilla-JS dashboard served by the Go binary, no frontend build

Status: Accepted

## Context

The system needs a web dashboard — agent list, metrics, alerts, deploy panel,
catalog, policy rules, settings, a floor-map editor. One maintainer, who is
primarily a Go/backend engineer.

A framework (React/Vue/Svelte) brings a Node toolchain, a `node_modules` tree, a
build step, a bundler config, and its own upgrade treadmill — a second stack to
own for an internal tool with a small, known set of screens.

## Decision

The dashboard is plain HTML, CSS, and JavaScript in `dashboard/` —
`index.html`, `app.js`, `style.css`. No framework, no bundler, **no build step**.

The Go server serves it directly: `GET /` returns `dashboard/index.html`,
`/static/*` serves the folder (`server/main.go`). Deploying the UI is copying the
`dashboard/` folder next to the server exe (`README.md`).

## Consequences

Easier:

- One toolchain. `go build` and you're done; there is no `npm install`, no
  `npm run build`, no separate frontend CI.
- Edit `app.js`, refresh the browser. No dev server, no HMR to configure.
- The UI is a static artifact — trivial to serve, trivial to reason about, no
  hydration or SSR concerns.
- Nothing to keep patched but the browser.

Harder:

- **No component model, no reactive state.** `app.js` does manual DOM updates and
  `fetch` calls. As screens grow (the floor-map editor became a drag-and-drop
  layout tool — commit `cf874a5`) this gets harder to keep tidy; state lives in
  ad hoc module variables.
- No TypeScript, so no compile-time checking on the API shapes the UI consumes —
  a renamed JSON field is found at runtime. (`docs/openapi.yaml` is the contract;
  nothing enforces the UI against it.)
- No ecosystem components — date pickers, tables, charts are hand-built or
  hand-picked as single files.
- Trades a known upfront cost (framework tooling) for a slowly-growing ongoing
  one (hand-rolled UI code) as the dashboard gets bigger.
