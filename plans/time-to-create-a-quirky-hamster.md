# Plan: botIAask GitHub Pages site (`github-web/`)

## Context
The project has no public website. We need a modern, open-source-style static site, hosted on GitHub Pages (repo `gcanosa/botIAask`), with light/dark themes taken from the two halves of `github-web/img/botIAask-logo.png`. It must cover install, Docker/Podman, standalone, daemon vs debug, docs, config template, changelog and releases. The whole site is plain HTML/CSS/JS with no build step, so Pages can serve the folder as is.

## Files (all under `github-web/`)
- `index.html`: landing page.
  - Hero with logo, tagline, install one-liner with a copy button, and badges (version, Go 1.26, license).
  - Feature grid: AI `!ask`, multi-network IRC, RSS, GitHub tracker, pastes/uploads, crypto/forex/weather, web dashboard, Channel Stats, SQLite/backups.
  - Quick-start tabs (Go / Docker / Podman), a dashboard section, a "latest release" card and a footer.
- `docs.html`: sticky sidebar plus scroll-spy docs, with these sections:
  - Requirements and installation: source build, `go run`, `go build .`.
  - Run modes: foreground/debug (default, `-debug=true`) vs daemon (`-daemon`, `-mode start|stop|restart`, `-dashboard`, `daemon.pid`, `-rehash`/SIGHUP). Shown as a comparison table.
  - Docker / Podman: `docker-compose.yml` walkthrough, `scripts/podman-build.sh`, volumes for state dirs, host networking for LM Studio, and the Docker Hub image `berkelioar/botiaask`.
  - Configuration: annotated sections (multi-network IRC, ai, bot, admin, web/auth, rss, github_tracker, stats, backup, omdb).
  - CLI flags table, IRC command reference (user/admin), GitHub tracker (token scopes, `!gh`), dashboard auth/CSRF, databases table.
- `changelog.html`: Releases and Changelog.
  - Releases: fetched at load from `api.github.com/repos/gcanosa/botIAask/releases`, falling back to `/tags`.
  - Changelog: a curated static list per tag (v0.2.1 to v0.4.3), built from `git log`.
  - Both have a static fallback if the API is rate-limited or offline.
- `config.html`: the config template viewer, with the full `config.yaml.template` in a highlighted block plus copy and download buttons.
- `config.yaml.template`: a copy of `config/config.yaml.template`, served for download and fetched by `config.html`.
- `assets/style.css`: design tokens, with dark/light via `prefers-color-scheme` plus a `[data-theme]` override.
- `assets/app.js`:
  - Theme toggle, saved in localStorage inside try/catch.
  - Copy-to-clipboard buttons, tabs, scroll-spy and the mobile nav.
  - GitHub API fetch with a static fallback.
  - A tiny YAML/code highlighter.
- `img/`: `logo-dark.png` (left half, for the dark theme) and `logo-light.png` (right half, for the light theme).
  - I'll crop the halves once with Python/Pillow (already installed, 12.3.0) from the 1774x887 source.
  - I'll also make a small favicon crop of the robot head.
  - The original PNG stays.
- `.nojekyll`.
- Repo root `.github/workflows/pages.yml`: uploads `github-web/` with `actions/upload-pages-artifact` and `actions/deploy-pages` on push to `main`.

## Design
- Accent blue `#1aa3ff`, taken from the logo. Dark theme `#05070c` with subtle glow gradients. Light theme `#f7f9fc`.
- Inter for text and JetBrains Mono for code, both from Google Fonts, plus system fallbacks.
- Sticky blurred navbar with the theme toggle and a GitHub link. Rounded cards, terminal-style code blocks with copy buttons, and scroll-reveal animations that respect `prefers-reduced-motion`.
- Responsive down to phone width, with accessible focus states and semantic HTML.
- Since both logo halves have solid backgrounds, I'll use the matching one per theme. If the background edges show, I'll switch to `mix-blend-mode` (`screen` on dark, `multiply` on light) so they blend in.

## Reused sources (content must match the repo)
- `README.md`: features and CLI table.
- `CLAUDE.md`: GitHub tracker, dashboard auth and databases.
- `config/config.yaml.template`, `Dockerfile`, `docker-compose.yml`, `DOCKHUB-compiling.txt`, `scripts/*.sh`.
- `main.go` flags, `meta/meta.go` (version 0.4.3), `internal/ircusage` for the command list.

## Verification
- Serve locally with `python3 -m http.server -d github-web`, then check with the Playwright MCP tools:
  - Screenshots of `index`, `docs`, `changelog` and `config` in both themes at desktop and 390px widths.
  - No console errors, and the theme toggle, copy buttons, tabs and scroll-spy work.
  - The changelog falls back cleanly with the network blocked.
- All internal links and image paths are relative, so they also work under `/botIAask/` on Pages.
- After push, the user enables Settings → Pages → Source: GitHub Actions. The site will be at `https://gcanosa.github.io/botIAask/`.

## Notes
- The workflow file and `git push` are outward-facing, so I'll only create the file. I won't commit or push unless asked.
- There are no GitHub Releases yet, only tags, so the Releases section will show tags until releases are published.
