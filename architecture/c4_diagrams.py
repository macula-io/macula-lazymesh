"""Generate macula-lazymesh's three C4 diagrams into assets/.

Run:  python3 architecture/c4_diagrams.py   (C4_OUT=<dir> writes elsewhere)

  c4-context.svg     Level 1: lazymesh, the operator and programs that drive it,
                     macula-mcp, the LLM providers and everything else it talks to
  c4-containers.svg  Level 2: the running parts -- the agent session, the TUI,
                     the control socket, the files -- and the child processes
                     (macula-mcp, the confined shell, git)
  c4-components.svg  Level 3: the packages inside the lazymesh process
                     (cmd/lazymesh, internal/, sdk) and what sits beside it.
                     Each monospace box title names packages:
                     architecture/check_c4_coverage.sh fails CI when a package
                     is missing from them and not in c4_exclusions.txt

Colours are plain classes with a prefers-color-scheme: dark block, no CSS
custom properties (rsvg-convert renders those black), so the SVGs render the
same on GitHub, in IDE previews and through rsvg-convert. The drawing helpers,
colours and legend are mcl-fovea's (macula-services/mcl-fovea,
architecture/c4_diagrams.py). After a change, render each
(rsvg-convert -w 1400 assets/c4-context.svg -o /tmp/c.png) and look at it;
keep architecture/README.md's tables in step.
"""
import os
from xml.sax.saxutils import escape

OUT = os.environ.get("C4_OUT") or os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "assets")

SANS = '"Atkinson Hyperlegible", "Segoe UI", system-ui, -apple-system, Helvetica, Arial, sans-serif'
MONO = '"JetBrains Mono", ui-monospace, Menlo, Consolas, monospace'

# Element kinds, each (light fill, light stroke, dark fill, dark stroke).
KINDS = {
    "b-sys": ("#F5E9CC", "#A56F0A", "#3A2E13", "#E3AA3C"),   # in scope
    "b-per": ("#E3EBF2", "#3B5B7A", "#1D2A36", "#8EB4D8"),   # person
    "b-mac": ("#E6E4EE", "#5E5A78", "#26243A", "#A9A4C8"),   # Macula infrastructure
    "b-ext": ("#ECEFED", "#8A9791", "#1C2421", "#7D8B85"),   # external system
    "b-cmp": ("#FFFFFF", "#8A9791", "#171F1C", "#7D8B85"),   # component or container
}

STYLE = f"""<style>
.t-name {{ font-family: {SANS}; font-size: 15px; font-weight: 700; }}
.t-desc {{ font-family: {SANS}; font-size: 12.5px; }}
.t-rel {{ font-family: {SANS}; font-size: 11.5px; paint-order: stroke; stroke-width: 4px; stroke-linejoin: round; }}
.t-code {{ font-family: {MONO}; font-size: 13px; font-weight: 600; }}
.t-type {{ font-family: {MONO}; font-size: 11.5px; }}
.t-bound {{ font-family: {MONO}; font-size: 12px; font-weight: 600; }}
.t-leg {{ font-family: {SANS}; font-size: 12px; }}
.edge {{ fill: none; stroke-width: 1.25; }}
.edge-later {{ fill: none; stroke-width: 1.25; stroke-dasharray: 5 4; }}
.bg {{ fill: #FFFFFF; }}
""" + "".join(f".{k} {{ fill: {v[0]}; stroke: {v[1]}; stroke-width: {2 if k == 'b-sys' else 1.25}; }}\n" for k, v in KINDS.items()) + """\
.b-later { fill: none; stroke: #8A9791; stroke-width: 1.25; stroke-dasharray: 5 4; }
.bound { fill: none; stroke: #B3BDB8; stroke-width: 1; }
.head { fill: #3B5B7A; }
.t-name, .t-code { fill: #16201D; }
.t-type, .t-bound, .t-leg { fill: #66736E; }
.t-desc { fill: #34413C; }
.t-rel { fill: #66736E; stroke: #FFFFFF; }
.edge, .edge-later { stroke: #8A9791; }
.arrowhead { fill: #8A9791; }
@media (prefers-color-scheme: dark) {
.bg { fill: #171F1C; }
""" + "".join(f".{k} {{ fill: {v[2]}; stroke: {v[3]}; }}\n" for k, v in KINDS.items()) + """\
.b-later { stroke: #7D8B85; }
.bound { stroke: #4A5752; }
.head { fill: #8EB4D8; }
.t-name, .t-code { fill: #E6ECE9; }
.t-type, .t-bound, .t-leg { fill: #93A09A; }
.t-desc { fill: #C3CDC8; }
.t-rel { fill: #93A09A; stroke: #171F1C; }
.edge, .edge-later { stroke: #7D8B85; }
.arrowhead { fill: #7D8B85; }
}
</style>"""

DEFS = ('<defs><marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
        'orient="auto-start-reverse"><path class="arrowhead" d="M0 0L10 5L0 10z"/></marker></defs>')

LEGEND = [("b-sys", "In scope"), ("b-per", "Person"), ("b-mac", "Macula infrastructure"),
          ("b-ext", "External system"), ("b-cmp", "Container or component"), ("b-later", "Planned, not built")]
BOUNDARY = ("bound", "Boundary")


class Diagram:
    def __init__(self, name, title, w, h):
        self.name, self.title, self.w, self.h, self.parts = name, title, w, h, []

    def text(self, cls, x, y, s, anchor=None, rotate=None):
        a = f' text-anchor="{anchor}"' if anchor else ""
        r = f' transform="rotate({rotate} {x} {y})"' if rotate is not None else ""
        self.parts.append(f'<text class="{cls}" x="{x}" y="{y}"{a}{r}>{escape(s)}</text>')

    def box(self, cls, x, y, w, h, name, kind, desc, person=False, code=False):
        self.parts.append(f'<rect class="{cls}" x="{x}" y="{y}" width="{w}" height="{h}" rx="10"/>')
        tx = x + 16
        if person:
            self.parts.append(f'<circle class="head" cx="{x + 26}" cy="{y + 24}" r="9"/>')
            tx = x + 44
        self.text("t-code" if code else "t-name", tx, y + 28, name)
        self.text("t-type", tx, y + 46, kind)
        for i, line in enumerate(desc):
            self.text("t-desc", x + 16, y + 70 + 16 * i, line)

    def bound(self, x, y, w, h, label):
        self.parts.append(f'<rect class="bound" x="{x}" y="{y}" width="{w}" height="{h}" rx="12"/>')
        self.text("t-bound", x + 16, y + 22, label)

    def edge(self, d, later=False):
        self.parts.append(f'<path class="{"edge-later" if later else "edge"}" d="{d}" marker-end="url(#a)"/>')

    def legend(self, y, boundary=True):
        x = 24
        for cls, label in LEGEND + ([BOUNDARY] if boundary else []):
            self.parts.append(f'<rect class="{cls}" x="{x}" y="{y}" width="22" height="14" rx="3"/>')
            self.text("t-leg", x + 30, y + 12, label)
            x += 34 + 6.6 * len(label)

    def write(self):
        svg = [(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" role="img" '
                f'aria-label="{escape(self.title)}">'), f"<title>{escape(self.title)}</title>", STYLE, DEFS,
               f'<rect class="bg" x="0" y="0" width="{self.w}" height="{self.h}"/>'] + self.parts + ["</svg>", ""]
        with open(os.path.join(OUT, self.name), "w") as f:
            f.write("\n".join(svg))


def context():
    d = Diagram("c4-context.svg", "macula-lazymesh system context", 1000, 640)
    d.box("b-per", 40, 30, 270, 116, "Operator", "[Person]",
          ["Watches rooms, rings and presence;", "talks to the agent, approves tool", "calls, types realm names to join"], person=True)
    d.box("b-ext", 365, 30, 270, 116, "Controller program", "[External: Go or Python SDK]",
          ["Drives --headless over a unix", "socket: input, events, rings, a", "pre-tool hook gating every call"])
    d.box("b-ext", 690, 30, 270, 116, "LLM provider", "[External: HTTPS API]",
          ["DeepSeek (default), NVIDIA, Groq", "(OpenAI-compatible) or Anthropic;", "key from an api_key_file"])
    d.box("b-ext", 40, 210, 270, 120, "Web pages", "[External: http/https]",
          ["What the web_fetch tool reads:", "bounded text, every address", "checked by an SSRF guard"])
    d.box("b-sys", 365, 210, 270, 120, "lazymesh", "[Go binary: TUI + agent harness]",
          ["An agent whose only job is mesh", "cooperation, with a lazygit-style", "live view of rooms and rings"])
    d.box("b-ext", 690, 210, 270, 120, "npm registry", "[External system]",
          ["Serves @macula-io/mcp to npx;", "its latest version, read by the", "startup update check"])
    d.box("b-ext", 40, 430, 270, 120, "Prometheus scraper", "[External, optional]",
          ["Reads /metrics when", "metrics_listen is set: turns, tool", "calls, errors, tokens"])
    d.box("b-mac", 365, 430, 270, 120, "macula-mcp", "[Macula, @macula-io/mcp >= 0.48.0]",
          ["The sole mesh tool source, a stdio", "subprocess; macula-mcp-realm runs", "realm joins outside the agent loop"])
    d.box("b-mac", 690, 430, 270, 120, "Macula mesh and realm", "[Macula infrastructure]",
          ["Stations and DHT, rooms, rings,", "other agents; mcl-rag and mcl-graph", "procedures; realm.macula.io"])
    d.edge("M310 100H328V236H365"); d.text("t-rel", 320, 180, "TUI", anchor="middle", rotate=-90)
    d.edge("M500 146V210"); d.text("t-rel", 508, 172, "unix socket, NDJSON;"); d.text("t-rel", 508, 188, "own user only")
    d.edge("M610 210V178H825V146"); d.text("t-rel", 680, 172, "chat completions")
    d.edge("M365 270H310"); d.text("t-rel", 337, 290, "fetches", anchor="middle")
    d.edge("M635 270H690"); d.text("t-rel", 662, 262, "version", anchor="middle"); d.text("t-rel", 662, 290, "check", anchor="middle")
    d.edge("M310 490H346V310H365"); d.text("t-rel", 354, 400, "scrapes")
    d.edge("M500 330V430"); d.text("t-rel", 508, 366, "spawns it with npx;"); d.text("t-rel", 508, 382, "MCP over stdio; execs"); d.text("t-rel", 508, 398, "macula-mcp-realm")
    d.edge("M635 490H690"); d.text("t-rel", 662, 482, "QUIC,", anchor="middle"); d.text("t-rel", 662, 510, "HTTPS", anchor="middle")
    d.legend(600, boundary=False)
    d.write()


def containers():
    d = Diagram("c4-containers.svg", "macula-lazymesh containers", 1000, 866)
    d.box("b-per", 40, 30, 280, 112, "Operator", "[Person]",
          ["Talks to the agent and answers", "approval prompts in the TUI"], person=True)
    d.box("b-ext", 360, 30, 280, 112, "LLM provider", "[External: HTTPS API]",
          ["DeepSeek, NVIDIA, Groq or", "Anthropic, streamed"])
    d.box("b-ext", 680, 30, 280, 112, "Controller program", "[External: Go or Python SDK]",
          ["Drives a session over the socket:", "input, events, approvals, the", "pre-tool hook"])
    d.bound(24, 176, 952, 334, "lazymesh process")
    d.box("b-cmp", 40, 210, 280, 116, "TUI", "[bubbletea; absent with --headless]",
          ["Rooms, rings, presence, chat pane,", "approval popups, realms view"])
    d.box("b-sys", 360, 210, 280, 116, "Agent session", "[Ergo actor, tool-calling loop]",
          ["One supervised conversation: model", "calls, the guard chain, tool calls;", "its events go to every frontend"])
    d.box("b-cmp", 680, 210, 280, 116, "Control socket", "[unix socket, NDJSON, 0600]",
          ["Served with --headless or", "--unix-socket; peer uid checked;", "the pre-tool hook fails closed"])
    d.box("b-cmp", 40, 370, 280, 116, "Session store", "[files: JSONL append logs]",
          ["One conversation per file, read", "back by --resume; the prompt", "history for the up key"])
    d.box("b-cmp", 360, 370, 280, 116, "Tool sources", "[in-process Go]",
          ["Mesh tools over MCP, web_fetch,", "file tools on an os.Root, git", "(own branch only), shell_exec"])
    d.box("b-cmp", 680, 370, 280, 116, "Config and logs", "[files]",
          ["config.yaml, the ring contact", "policy, trusted checkout notes,", "agent.log; /metrics if enabled"])
    d.box("b-mac", 40, 560, 280, 124, "macula-mcp", "[child: npx, Node, MCP over stdio]",
          ["The sole mesh tool source, for the", "whole session; realm joins run as a", "separate macula-mcp-realm child"])
    d.box("b-cmp", 360, 560, 280, 124, "Confined shell", "[child: bubblewrap, per command]",
          ["Each approved shell_exec: checkout", "and scratch read-write, toolchain", "read-only, no network, no env"])
    d.box("b-cmp", 680, 560, 280, 124, "git", "[child: fixed argv, per call]",
          ["Commit and push the agent's own", "branch on the checkout's remote;", "never main, master or a tag"])
    d.box("b-mac", 40, 730, 600, 72, "Macula mesh and realm", "[Macula infrastructure: QUIC, HTTPS]", [])
    d.edge("M180 142V210"); d.text("t-rel", 188, 164, "keys, terminal")
    d.edge("M500 210V142"); d.text("t-rel", 508, 164, "chat completions")
    d.edge("M820 142V210"); d.text("t-rel", 828, 164, "unix socket, NDJSON")
    d.edge("M360 268H320"); d.text("t-rel", 340, 260, "events", anchor="middle")
    d.edge("M640 268H680"); d.text("t-rel", 660, 260, "events", anchor="middle")
    d.edge("M500 326V370"); d.text("t-rel", 508, 352, "tool calls")
    d.edge("M360 300H340V428H320"); d.text("t-rel", 332, 364, "appends", anchor="middle", rotate=-90)
    d.edge("M400 486V530H180V560"); d.text("t-rel", 188, 550, "MCP over stdio")
    d.edge("M500 486V560"); d.text("t-rel", 508, 550, "shell_exec")
    d.edge("M600 486V530H820V560"); d.text("t-rel", 828, 550, "git_commit, git_push")
    d.edge("M180 684V730"); d.text("t-rel", 188, 712, "QUIC, HTTPS")
    d.legend(830)
    d.write()


def components():
    d = Diagram("c4-components.svg", "macula-lazymesh components", 1000, 1220)
    d.bound(16, 40, 968, 996, "lazymesh process [Go 1.27, cmd/lazymesh, internal/]")
    d.box("b-sys", 32, 70, 936, 80, "cmd/lazymesh", "[main.go, eventbridge.go]",
          ["Loads config, builds the provider and the tool chain, spawns macula-mcp, starts the session; the event bridge fans events to the TUI and the socket."], code=True)
    d.box("b-cmp", 32, 174, 300, 112, "sessionhost", "[Ergo node, simple_one_for_one]",
          ["A supervised session actor per", "conversation, restarted on a crash", "with the same args"], code=True)
    d.box("b-cmp", 350, 174, 300, 112, "agent", "[tool-calling loop; Outlet]",
          ["Converses, calls tools, feeds results", "back; an Outlet per consumer keeps", "events in order and loses none"], code=True)
    d.box("b-cmp", 668, 174, 300, 112, "provider", "[LLM backends]",
          ["deepseek, nvidia, groq on the", "OpenAI-compatible shape; anthropic", "on the Messages API; streamed"], code=True)
    d.box("b-cmp", 32, 310, 936, 96, "toolchain, rostergate", "[the one guard chain, both profiles]",
          ["Every tool call passes denial, refuse, hook, ask, terse, handoff, watched rooms and the allowlist, in one declared order;",
           "the roster gate lets the model see only room messages the station attests came from a node on the operator's roster."], code=True)
    d.bound(32, 430, 936, 162, "Tool sources")
    d.box("b-cmp", 48, 462, 190, 116, "mcpclient", "[the sole mesh source]",
          ["Spawns macula-mcp; one", "MCP session for the agent", "and the TUI; a call log"], code=True)
    d.box("b-cmp", 254, 462, 222, 116, "meshservices, webfetch", "[off by default; always]",
          ["mcl-rag and mcl-graph", "procedures from the DHT;", "web_fetch, SSRF-guarded"], code=True)
    d.box("b-cmp", 492, 462, 240, 116, "localtools, confinedshell", "[opt-in, allowlist-gated]",
          ["File tools on an os.Root;", "shell_exec inside bubblewrap,", "self-tested before offered"], code=True)
    d.box("b-cmp", 748, 462, 204, 116, "gittools", "[coding profile]",
          ["git_branch, git_commit,", "git_push: own branch only,", "fixed argv, no force"], code=True)
    d.box("b-cmp", 32, 628, 222, 112, "roomwaiter, ringwaiter", "[loop-owned waits]",
          ["Block in mesh_wait_room and", "mesh_wait_ring; arrivals", "come in as prompts"], code=True)
    d.box("b-cmp", 270, 628, 222, 112, "scheduler", "[deferred prompts]",
          ["The socket's schedule", "message arms a prompt for", "a later wakeup"], code=True)
    d.box("b-cmp", 508, 628, 222, 112, "tui, termkeys", "[bubbletea, lipgloss]",
          ["Status strip, panels, chat", "pane, ring and approval", "popups, realms view"], code=True)
    d.box("b-cmp", 746, 628, 222, 112, "frontend", "[unix-socket control plane]",
          ["NDJSON both ways, 0600,", "peer uid checked; pre-tool", "hook, failing closed"], code=True)
    d.box("b-cmp", 32, 764, 300, 104, "sessionstore", "[JSONL append logs]",
          ["One conversation per file,", "O_APPEND; --resume loads one"], code=True)
    d.box("b-cmp", 350, 764, 300, 104, "prompthistory", "[per-checkout JSONL]",
          ["The prompts an operator sent,", "given back with the up key"], code=True)
    d.box("b-cmp", 668, 764, 300, 104, "checkoutnotes", "[AGENTS.md, CLAUDE.md as data]",
          ["A checkout's notes, never orders", "unless the operator trusted them"], code=True)
    d.box("b-cmp", 32, 892, 300, 112, "realmjoin, updatecheck", "[subprocess; HTTPS]",
          ["Execs macula-mcp-realm join,", "never a tool; reads npm's", "latest @macula-io/mcp"], code=True)
    d.box("b-cmp", 350, 892, 300, 112, "config, contactpolicy", "[YAML; policy file]",
          ["config.yaml; who may ring this", "agent, from the contact policy"], code=True)
    d.box("b-cmp", 668, 892, 300, 112, "logging, counters", "[agent.log; /metrics]",
          ["agent.log; runtime counters,", "served on /metrics when", "metrics_listen is set"], code=True)
    d.edge("M182 150V174"); d.text("t-rel", 190, 166, "starts the session")
    d.edge("M332 230H350")
    d.edge("M650 230H668")
    d.edge("M500 286V310"); d.text("t-rel", 508, 302, "tool calls")
    d.edge("M500 406V430"); d.text("t-rel", 508, 422, "past every guard")
    d.edge("M143 628V592"); d.text("t-rel", 151, 614, "wait calls")
    d.edge("M48 520H24V1108H32"); d.text("t-rel", 11, 816, "MCP over stdio", anchor="middle", rotate=-90)
    d.edge("M182 1004V1060"); d.text("t-rel", 190, 1046, "execs")
    d.edge("M968 1108H976V684H968"); d.text("t-rel", 991, 896, "unix socket", anchor="middle", rotate=90)
    d.box("b-mac", 32, 1060, 456, 96, "macula-mcp subprocess", "[npx -y -p @macula-io/mcp, >= 0.48.0]",
          ["The mesh: rooms, rings, presence, calls, DHT;", "macula-mcp-realm for joins, a separate process"])
    d.box("b-cmp", 512, 1060, 456, 96, "sdk", "[Go client.go, Python lazymesh_client.py]",
          ["A controller's side of the socket: Say, events,", "pending rings, HookPreTool and ServeHook"], code=True)
    d.legend(1188)
    d.write()


if __name__ == "__main__":
    for make in (context, containers, components):
        make()
