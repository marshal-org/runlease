#!/usr/bin/env python3
"""Generates site/demo.svg: a looping clip with the terminal on the left and the
architecture it builds on the right. Pure SVG and CSS, so it plays in an <img>
(the website, and the README on GitHub) with no script and no video file.

Usage: scripts/gen-demo-clip.py site/demo.svg [FREEZE_SECONDS]   (freeze = a still frame, for checking)
"""
import sys, html

if len(sys.argv) < 2:
    print("Usage: scripts/gen-demo-clip.py site/demo.svg [FREEZE_SECONDS]", file=sys.stderr)
    sys.exit(2)
OUT = sys.argv[1]
FREEZE = float(sys.argv[2]) if len(sys.argv) > 2 else None
T = 30.0          # one loop, seconds
END = 28.6        # everything is cleared by here, then a short pause

css, body, n = [], [], [0]

def pct(t): return f"{max(0.0, min(100.0, t / T * 100)):.3f}%"

def anim(kind, frames, steps=None):
    """Registers one keyframe set and returns the class that plays it."""
    n[0] += 1
    name = f"{kind}{n[0]}"
    css.append("@keyframes %s{%s}" % (name, "".join(f"{pct(t)}{{{v}}}" for t, v in frames)))
    timing = f"steps({steps},end)" if steps else "linear"
    css.append(f".{name}{{animation:{name} {T}s {timing} infinite}}")
    return name

def show(on, off=END, fade=0.25):
    """Visible from `on` to `off`."""
    return anim("s", [(0, "opacity:0"), (on, "opacity:0"), (on + fade, "opacity:1"),
                      (off, "opacity:1"), (off + fade, "opacity:0"), (T, "opacity:0")])

def esc(s): return html.escape(s, quote=False)

# ── terminal ────────────────────────────────────────────────────────────────
TX, TY, TW, TH = 20, 20, 560, 560
LH, FS, CW = 31, 15.5, 9.32          # line height, font size, width of one character
body.append(f'<rect x="{TX}" y="{TY}" width="{TW}" height="{TH}" rx="12" class="term"/>')
body.append(f'<circle cx="{TX+24}" cy="{TY+22}" r="5.5" fill="#ff5f57"/><circle cx="{TX+43}" cy="{TY+22}" r="5.5" fill="#febc2e"/><circle cx="{TX+62}" cy="{TY+22}" r="5.5" fill="#28c840"/>')
body.append(f'<text x="{TX+TW/2}" y="{TY+27}" class="ttitle" text-anchor="middle">your terminal</text>')

row = [0]
def y(): return TY + 74 + row[0] * LH

def command(text, start, dur):
    """A typed line: the prompt appears, then a cover shrinks away to the right."""
    yy = y(); row[0] += 1
    x0 = TX + 24
    body.append(f'<g class="{show(start - 0.3)}"><text x="{x0}" y="{yy}" class="tp">$</text>'
                f'<text x="{x0 + 2*CW}" y="{yy}" class="tc">{esc(text)}</text>')
    w = len(text) * CW + 8
    cover = anim("c", [(0, "transform:scaleX(1)"), (start, "transform:scaleX(1)"),
                       (start + dur, "transform:scaleX(0)"), (T, "transform:scaleX(0)")], steps=None)
    # steps() per segment would restart at each keyframe; a fine linear wipe reads as typing.
    body.append(f'<rect x="{x0 + 2*CW - 2}" y="{yy - FS}" width="{w:.1f}" height="{LH - 6}" class="cover {cover}"/></g>')

def output(text, at, cls="to"):
    yy = y(); row[0] += 1
    body.append(f'<text x="{TX+24}" y="{yy}" class="{cls} {show(at)}" xml:space="preserve">{esc(text)}</text>')

command("runlease up --provider gcp --ttl 2h", 0.7, 1.9)
output("Pre-registering relay slot...", 3.0)
output("Requesting spot instance (e2-small)...", 6.0)
output("Instance ready", 9.5, "tok")
output("   Public IP: none", 9.8)
output("   Rate:      ~$0.026/hr", 10.1)
output("Agent connected", 12.4)
row[0] += 0.4
command("runlease exec dev -- make test", 14.2, 1.7)
output("ok    all tests passed    12.4s", 17.1, "tok")
row[0] += 0.4
command("runlease snooze dev 1h", 19.2, 1.3)
output("dev now auto-terminates in 3h0m0s", 21.1)
row[0] += 0.4
output("# nobody runs 'down'. the lease just ends.", 23.0, "tdim")
command("runlease list", 25.0, 0.8)
output("No active instances tracked on this computer.", 26.3)

# ── architecture ────────────────────────────────────────────────────────────
AX = 610
def node(x, yy, w, h, title, sub, cls, extra=""):
    return (f'<g class="{cls}"><rect x="{x}" y="{yy}" width="{w}" height="{h}" rx="10" class="node"{extra}/>'
            f'<text x="{x+18}" y="{yy+34}" class="nt">{esc(title)}</text>'
            f'<text x="{x+18}" y="{yy+58}" class="ns">{esc(sub)}</text></g>')

def chip(x, yy, text, cls, kind="chip"):
    w = len(text) * 7.3 + 22
    return (f'<g class="{cls}"><rect x="{x}" y="{yy}" width="{w:.0f}" height="24" rx="12" class="{kind}"/>'
            f'<text x="{x+11}" y="{yy+16.5}" class="{kind}t">{esc(text)}</text></g>')

def draw(d, length, a, b, off, cls="wire"):
    k = anim("d", [(0, f"stroke-dashoffset:{length};opacity:0"), (a, f"stroke-dashoffset:{length};opacity:1"),
                   (b, "stroke-dashoffset:0;opacity:1"), (off, "stroke-dashoffset:0;opacity:1"),
                   (off + 0.3, "stroke-dashoffset:0;opacity:0"), (T, f"stroke-dashoffset:{length};opacity:0")])
    return f'<path d="{d}" class="{cls} {k}" stroke-dasharray="{length}" pathLength="{length}"/>'

# captions: one per step, above the diagram
caps = [(0.8, 3.0, "1  Keys are made on your computer. They never leave it."),
        (3.0, 6.0, "2  A slot is reserved at the relay. The relay holds no key."),
        (6.0, 10.6, "3  A spot machine starts in your own cloud account."),
        (10.6, 14.0, "4  It dials out. No public address, no open port."),
        (14.0, 19.0, "5  SSH runs end to end. The relay sees only ciphertext."),
        (19.0, 22.8, "6  Need longer? Snooze moves the deadline."),
        (22.8, END, "7  The lease ends and the machine deletes itself.")]
for a, b, text in caps:
    body.append(f'<text x="{AX+20}" y="52" class="cap {show(a, b - 0.3)}">{esc(text)}</text>')

# your computer
body.append(node(AX+20, 86, 232, 96, "Your computer", "runs the CLI", show(0.3)))
body.append(chip(AX+38, 192, "SSH keys made here", show(2.4), "chipa"))

# relay
body.append(node(AX+312, 232, 236, 96, "Relay", "pairs the two sides", show(3.0)))
body.append(chip(AX+330, 338, "slot reserved", show(4.3, 14.3)))
body.append(chip(AX+330, 338, "sees only ciphertext", show(14.6, 22.8), "chipg"))

# your cloud account
body.append(f'<g class="{show(6.0)}"><rect x="{AX+20}" y="400" width="528" height="176" rx="12" class="zone"/>'
            f'<text x="{AX+38}" y="426" class="zt">YOUR CLOUD ACCOUNT</text></g>')
vm_gone = 24.0
body.append(node(AX+44, 440, 250, 96, "Spot machine", "e2-small · billed to you", show(6.6, vm_gone)))
body.append(chip(AX+312, 446, "no public address", show(9.8, vm_gone)))
body.append(chip(AX+312, 478, "lease 2:00", show(10.1, 21.1), "chipa"))
body.append(chip(AX+312, 478, "lease 3:00", show(21.2, 22.9), "chipa"))
body.append(chip(AX+312, 478, "lease over", show(23.0, vm_gone), "chipr"))
body.append(f'<text x="{AX+62}" y="496" class="gone {show(24.5)}">deleted itself. nothing left to pay for.</text>')

# wires: computer -> relay, machine -> relay (both outbound), then the tunnel
cx, cy = AX+136, 182          # under the computer
rx_, ry = AX+312, 280         # left of the relay
body.append(draw(f"M{cx} {cy+38} V{ry} H{rx_}", 100, 3.3, 4.2, 22.9))
mx, my = AX+169, 440          # top of the machine
rbx, rby = AX+430, 328        # under the relay
body.append(draw(f"M{mx} {my} V386 H{rbx} V{rby+38}", 100, 10.8, 11.8, 22.9))
body.append(f'<text x="{cx+12}" y="262" class="wl {show(3.6, 22.9)}">outbound</text>')
body.append(f'<text x="{mx+12}" y="378" class="wl {show(11.0, 22.9)}">dials out</text>')
tun = anim("t", [(0, "opacity:0;stroke-dashoffset:0"), (14.3, "opacity:0;stroke-dashoffset:0"),
                 (14.7, "opacity:1;stroke-dashoffset:-8"), (22.7, "opacity:1;stroke-dashoffset:-168"),
                 (23.0, "opacity:0;stroke-dashoffset:-174"), (T, "opacity:0;stroke-dashoffset:-174")])
body.append(f'<path d="M{cx} {cy+38} V{ry} H{rx_} M{rbx} {rby+38} V386 H{mx} V{my}" class="tunnel {tun}"/>')
body.append(f'<text x="{AX+312}" y="216" class="tl {show(14.8, 22.8)}">SSH, encrypted end to end</text>')

freeze = f"*{{animation-delay:-{FREEZE}s!important;animation-play-state:paused!important}}" if FREEZE is not None else ""

svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 600" role="img" aria-labelledby="t d">
<title id="t">Runlease in thirty seconds</title>
<desc id="d">A terminal runs runlease up, exec, snooze and list. Beside it a diagram builds step by step: keys are made on your computer, a slot is reserved at the relay, a spot machine starts in your cloud account and dials out, SSH runs end to end through the relay, the lease is extended, and the machine deletes itself.</desc>
<style>
text{{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace}}
.term{{fill:#0c1a2b}}
.ttitle{{fill:#6f84a0;font-size:12.5px}}
.tp{{fill:#f2a33a;font-size:{FS}px}}
.tc{{fill:#eaf1fb;font-size:{FS}px}}
.to{{fill:#b9c8dc;font-size:{FS}px}}
.tok{{fill:#5fd38d;font-size:{FS}px}}
.tdim{{fill:#6f84a0;font-size:{FS}px}}
.cover{{fill:#0c1a2b;transform-box:fill-box;transform-origin:right center}}
.cap{{fill:#0f1c2e;font-size:15px;font-weight:700;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}}
.node{{fill:#fff;stroke:#b9c6d8;stroke-width:1.5}}
.nt{{fill:#0f1c2e;font-size:17px;font-weight:700;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}}
.ns{{fill:#5b6b82;font-size:13px;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}}
.zone{{fill:none;stroke:#8ea0ba;stroke-width:1.5;stroke-dasharray:7 6}}
.zt{{fill:#5b6b82;font-size:11.5px;letter-spacing:.12em}}
.chip{{fill:#e9eef6}}.chipt{{fill:#33445c;font-size:12px}}
.chipa{{fill:#fdecc8}}.chipat{{fill:#8a5200;font-size:12px}}
.chipg{{fill:#d9f3e3}}.chipgt{{fill:#12693a;font-size:12px}}
.chipr{{fill:#fbdcd8}}.chiprt{{fill:#98261b;font-size:12px}}
.wire{{fill:none;stroke:#7d90ab;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}}
.wl{{fill:#5b6b82;font-size:12px}}
.tunnel{{fill:none;stroke:#1f9d55;stroke-width:4.5;stroke-linecap:round;stroke-linejoin:round;stroke-dasharray:10 8}}
.tl{{fill:#12693a;font-size:12.5px;font-weight:700}}
.gone{{fill:#5b6b82;font-size:13.5px;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}}
@media (prefers-color-scheme:dark){{
.cap,.nt{{fill:#e8eff9}}.node{{fill:#13233a;stroke:#2f4666}}.ns,.zt,.wl,.gone{{fill:#9db0c9}}
.zone{{stroke:#4a6184}}.wire{{stroke:#6f86a8}}.chip{{fill:#20324d}}.chipt{{fill:#c6d4e8}}
.chipa{{fill:#4a3410}}.chipat{{fill:#ffcf7a}}.chipg{{fill:#12402a}}.chipgt{{fill:#7fe0a8}}
.chipr{{fill:#4d1d19}}.chiprt{{fill:#ffada3}}.tunnel{{stroke:#3ecf7d}}.tl{{fill:#7fe0a8}}
}}
{"".join(css)}
{freeze}
@media (prefers-reduced-motion:reduce){{
/* A still picture: the session running, before the lease ends. */
*{{animation:none!important}}
[class*=" s"],[class^="s"],[class*=" d"],[class*=" t"]{{opacity:1}}
.cover{{opacity:0}}
}}
</style>
{chr(10).join(body)}
</svg>
'''
open(OUT, "w", encoding="utf-8").write(svg)
print(OUT, len(svg), "bytes")
