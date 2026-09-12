# lazymesh — Editor Plugins Plan (lazymesh.nvim, then lazymesh.vscode)

**Status:** Draft — awaiting approval
**Created:** 2026-09-12
**Last Updated:** 2026-09-12 (revised: same-repo placement; GUI belongs to the Hecate line)

## Why this exists

> So a human can watch and drive the mesh from the tool they already live
> in (Neovim first, VS Code after), using `lazymesh` the same way lazygit
> users use lazygit — without a Lua port of the mesh ever being built, and
> without lazymesh pretending to be a GUI product.

## Decisions recorded here

1. **Same repo: plugins live in `macula-io/macula-lazymesh`** as
   `plugins/nvim/` and later `plugins/vscode/`. No separate
   `macula-plugins` monorepo: the plugin's value is "run this binary
   nicely", its bridge API version-locks to the harness's stdio interface,
   and one owner owns both ends. A plugins monorepo is a horizontal layer;
   it earns its existence only when a second product needs editor plugins.
2. **No `macula-lua` SDK.** The Lua layer is a thin UI/RPC shell; all mesh
   capability comes from the `lazymesh` Go binary (which already drives
   `macula-mcp` as its only tool source). Mirrors lazygit.nvim wrapping
   lazygit, octo.nvim wrapping `gh`.
3. **One process, reused.** The nvim bridge talks to `lazymesh` directly
   (jobstart/stdio), not to `macula-mcp` — one spawned process gives the
   plugin both the TUI and headless mesh operations.
4. **The engine/frontend split is the seam.** `lazymesh` grows a headless
   stdio control API (its TUI becomes one frontend among several). This
   API is what nvim, VS Code AND the future Hecate-line GUI all drive —
   one seam, three consumers.

## The GUI question is settled by convergence — and out of scope here

Every GUI discussion this session produced the same architecture:
web-first dashboard, thin native shell, daemon-owned assets. That is
literally what `hecate-social/hecate-web` already was: a Tauri v2 shell
proxying `hecate://` to hecate-daemon's Unix socket, with the daemon
serving a SvelteKit SPA from `priv/static/` (SSE streaming, per-plugin
permissions).

Consequences recorded here so the circling stops:

- **No `macula-electron`.** The desktop-shell question is answered by
  hecate-web's Tauri shape whenever the Hecate line is revived.
- **No standalone mesh-dashboard repo.** The mesh view belongs as a
  section/plugin of the Hecate runtime's web UI (reachable from a Tauri
  window and a plain browser alike), driving lazymesh's stdio API.
- **`hecate-web` and `hecate-daemon` are archived** (2026-09-02) and the
  workspace rules say not to build on the daemon. Reviving the Hecate
  runtime line is a product decision recorded here as an open question,
  deliberately NOT part of this plan.
- lazymesh therefore stays what it is — engine + TUI + cheap editor
  embeddings — and the full GUI is a Hecate-line product decision.

## What each plugin is

### lazymesh.nvim (`plugins/nvim/`)

- **Float TUI** — open `lazymesh` in a float/terminal, lazygit.nvim style
  (`<leader>gm` proposed; sits alongside the `<leader>g*` git family).
- **Headless bridge** — `vim.fn.jobstart("lazymesh", ...)` over the
  stdio API, exposing a small typed Lua API:
  - `require("lazymesh").publish(topic, fact)` — e.g. an autocmd that
    publishes a `commit_made` fact when an agent commits in this repo
  - `.recall(query)`, `.rooms()`, `.inbox()`, `.lobby()`
- **Binary discovery**, in order: `lazymesh` on `PATH` → Mason-installed
  `lazymesh` package → prompt offering `install.sh` (the existing
  installer) → graceful "not installed" state.
- **Config**: `require("lazymesh").setup({ binary = "lazymesh", float = {...} })`,
  lazy-loaded on command/key.

### lazymesh.vscode (`plugins/vscode/`, Phase 4)

- Same philosophy: extension launches `lazymesh` in an integrated terminal
  tab and contributes a handful of commands (Open lazymesh, Publish fact,
  Recall) — no mesh logic in the extension. A webview dashboard is
  explicitly out of scope here; that surface belongs to the Hecate line.

## How these plugins reach registries

**Neovim has no central marketplace.** The distribution reality, in order
of usefulness:

| Channel | What goes there | How |
|---------|-----------------|-----|
| GitHub (`macula-io/macula-lazymesh`) + Releases | the plugin itself; canonical source | tags + semver; README carries the lazy.nvim spec using the subdirectory form: `{ "macula-io/macula-lazymesh", dir = "plugins/nvim", lazy = true }` |
| **Mason** (mason-ng registry) | the `lazymesh` **binary**, not the Lua plugin | registry PR pointing at release assets; users get `:MasonInstall lazymesh` and the plugin auto-detects it — the closest thing nvim has to a "registry install" for a plugin that needs a binary |
| dotfyle.com | discoverability | submit the plugin profile |
| awesome-neovim (rockerBOO) | discoverability | list PR |
| rocks.nvim / rocks.dev | optional secondary channel | luarocks package possible later; awkward for the bundled-binary case, so not Phase 1 |

**VS Code has a real registry:** **Open VS X** (open-vsx.org) — the
registry VSCodium/Cursor/other editors actually consume, and the right
primary target. Publish the `.vsix` with the `ovsx` CLI from CI; also
attach `.vsix` to GitHub Releases for manual installs. The Microsoft
Marketplace is optional and only if/when Raf registers a publisher
account. Open VSX first, GitHub Releases always.

**CI does the publishing work**: one workflow — build plugin, verify
`lazymesh` binary presence/download, tag releases; a second job publishes
`.vsix` to Open VSX and opens/updates the mason registry PR. Registries
are fed from releases, never hand-uploaded.

## Phases

- [ ] **Phase 0 — Engine seam**: `lazymesh` gains a headless stdio mode
      (`--stdio` / `--headless`), exposing publish/recall/rooms/inbox/
      lobby/status as newline-delimited JSON — the API nvim, VS Code and
      the Hecate line all consume. TUI behavior unchanged.
- [ ] **Phase 1 — nvim scaffold**: `plugins/nvim/` in macula-lazymesh
      (plugin/, lua/lazymesh/, doc/, README with install spec + keymaps);
      CI skeleton; LICENSE (Apache-2.0 OR MIT, matching the repo).
- [ ] **Phase 2 — Float TUI**: lazygit.nvim-style float wrapping
      `lazymesh`; binary discovery (PATH → Mason → install prompt);
      `<leader>gm` keymap; vimdoc help.
- [ ] **Phase 3 — Headless bridge**: jobstart over the stdio API;
      `publish/recall/rooms/inbox/lobby` Lua API; one example autocmd
      (commit → publish `commit_made` fact) as the dogfood path.
- [ ] **Phase 4 — Distribution**: mason registry package for the
      `lazymesh` binary; dotfyle + awesome-neovim submissions; release
      tagging workflow.
- [ ] **Phase 5 — lazymesh.vscode**: Open VSX publish, integrated
      terminal launch + contributed commands; `.vsix` on Releases.

## Files to Create/Modify

| File | Purpose | Status |
|------|---------|--------|
| `macula-lazymesh/cmd/lazymesh/` | Phase 0: stdio/headless mode | Not started |
| `macula-lazymesh/plugins/nvim/` | nvim plugin (plugin/, lua/, doc/) | Not started |
| `macula-lazymesh/plugins/vscode/` | VS Code extension | Not started |
| `macula-lazymesh/README.md` | Cross-link plugin + install story | Not started |

## Success Criteria

- [ ] `{ "macula-io/macula-lazymesh", dir = "plugins/nvim" }` in any
      lazy.nvim config + `<leader>gm` opens the lazymesh TUI with zero
      manual binary steps when Mason is present.
- [ ] `:LazymeshPublish topic fact` works headlessly from within nvim,
      with the fact observable by other agents on the mesh.
- [ ] The commit→fact autocmd example runs and is documented.
- [ ] `:MasonInstall lazymesh` resolves to the same release binary as
      `install.sh`.
- [ ] The stdio API is consumed by at least one non-nvim client (the
      dogfood proof that the seam, not the editor, is the interface).
- [ ] `lazymesh.vscode` installable from Open VSX by extension ID.

## Open questions

- **Hecate-line revival**: when, and under whose plan? The mesh dashboard
  belongs there; this plan deliberately does not include it. (Workspace
  rules: do not build on the archived hecate-daemon.)
- Microsoft Marketplace publisher account — yes/no? (Open VSX is
  sufficient either way.)
- Mason package naming: `lazymesh` binary, package `lazymesh` — confirm
  no collision in mason-ng registry.
- VSCode extension id on Open VSX: `macula-io.lazymesh`?
