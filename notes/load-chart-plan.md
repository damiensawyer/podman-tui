# Load Chart — Architecture & Implementation Plan

> **Status: IMPLEMENTED** (milestones 1–6 complete, branch `load_chart`).
> 6a by-process mode · 6b entity toggle ('t') · 6c connection-loss banner.
> All suites green (`ui/charts` 62 specs, `ui/loadchart` 10 specs, `pdcs/hoststats`),
> `-race` clean; ui/charts is podman-import-free and type-checks on linux/windows/darwin.

Live multi-series CPU/memory load chart for `podman-tui`, inspired by **lazydocker**'s stats tab and **btop**'s high-resolution multi-symbol canvas engine.

Unlike lazydocker (which stacks isolated single-series charts per container), this draws **all series together on a shared canvas** with common axes, dynamic scaling, interactive entity toggling, and live legend sparklines.

The chart is invocable from three primary locations:

| Where you are        | Menu entry   | Scope of the chart                              |
|----------------------|--------------|-------------------------------------------------|
| System page (F2)     | `load chart` | Whole system (all pods/containers on host/dest) |
| Pods page (F3)       | `load chart` | The **selected pod** only                       |
| Containers page (F4) | `load chart` | The **selected container** only                 |

---

## 1. View Modes & Controls

### Independent Selectors (Dropdowns / Hotkeys)
- **GROUP BY** (`g`): What an individual curve represents
- **METRIC** (`m`): `CPU %` or `Memory` (auto-scaled MB/GB or % of host)
- **SYMBOL / RESOLUTION** (`s`): `Braille` (high-res 2x4) | `Block` (medium-res 2x2) | `TTY` (ASCII/shading fallback)

### Scope Availability Matrix

| Scope      | Total (`total`)    | By Pod (`by-pod`) | By Container (`by-container`) | By Process (`by-process`)           |
|------------|--------------------|-------------------|-------------------------------|-------------------------------------|
| system     | 1 line (aggregate) | 1 line per pod    | 1 line per container          | Top-N processes across containers   |
| pod        | 1 line (pod total) | —                 | 1 line per container in pod   | Processes of the pod's containers   |
| container  | 1 line             | —                 | —                             | Processes inside container          |

*Note: "Total" is always available; "By Pod" only at system scope; "By Process" is available at every scope (CPU-only).*

### Extra Interactive Controls
- **Symbol Mode Toggle (`s`)**: Cycles between `braille` $\leftrightarrow$ `block` $\leftrightarrow$ `tty` on-the-fly to adapt to any terminal/font capabilities.
- **Entity Toggle (`t`)**: Checkable table of pods/containers/processes to show or hide in the chart. Hidden series continue collecting in background ring buffers so re-enabling has no historical gaps.
- **Pause (`p`)**: Freezes scrolling and frame ingestion.
- **History**: 60 samples window at 1 sample/s (matches `utils.RefreshInterval`, and requires `StatsOptions.Interval = 1` — see §2.2). Collection is 1 sample/s, so Braille's 2 sub-columns per cell do **not** double the sample count: a 60-sample window occupies 30 cells at full resolution, or the line rasteriser interpolates between samples to span 60 cells. Default is to interpolate (one sample per cell, the second sub-column is the Bresenham midpoint).
- **Hotkey dispatch**: `tview.DropDown` consumes letter keys for prefix search while focused, so the dialog's `InputHandler` must check `g/m/s/t/p/Esc` **before** delegating to the focused widget. The app's global `SetInputCapture` already skips rune handling while a dialog is active, so there is no clash with page-switch keys.

---

## 2. Architecture — Three Shared Layers

The new package lives under `ui/charts/`. The canvas and primitive layers contain **zero podman-specific dependencies** so they can be reused for infobar sparklines, dashboard widgets, or daemon monitors.

```
ui/charts/
  canvas.go      MicroCanvas: 2x4 sub-cell grid, BTOP lookup tables, line rasterizer
  chart.go       Chart primitive: canvas + N series + ring buffers + axes + legend
  series.go      Series model: ID, name, color, 60-sample ring buffer, window stats
  scope.go       Scope/ScopeTarget/GroupMode types and series id scheme
  collector.go   Background collector engine (podman agnostic, seam wired)
  colors.go      Palette: 12 high-contrast hex colors, assigned by first appearance

ui/loadchart/
  defaults.go    Podman glue: adapters entities -> charts types, default wiring
  loadchart.go   LoadChartDialog: tview dialog shell, hotkeys, control dropdowns
```

Supporting pdcs changes (podman-aware, outside `ui/charts/`):

```
pdcs/containers/stats.go   StatsStream(ctx, ids, interval) — multi-ID streaming wrapper
pdcs/sysinfo/sysinfo.go    expose MemTotal (bytes) on SystemInfo
pdcs/hoststats/            NEW package: local cgroup/proc CPU reader (NOT `pdcs/system`,
                           which would clash with the podman bindings `system` package
                           already imported by sysinfo)
```

Lint note: `.golangci.yml` enables every linter (`default: all`). The lookup tables and chart geometry will need targeted `//nolint:mnd` (and the `require-specific` rule means bare `//nolint` is rejected). Budget for this in milestone 1.

Layout amendment (milestone 3): the collector engine lives in `ui/charts/collector.go` but is **podman agnostic** — it consumes podman free `ContainerSample`/`ContainerInfo` types through replaceable function seams. All podman glue (bindings stats stream, container listing, sysinfo, hoststats wiring) lives in the new `ui/loadchart` package, which will also host the dialog shell (milestone 4). Rationale: `ui/charts` then has zero podman imports end to end, so the whole package type checks on every GOOS — vendored cgo chains (buildah/define -> gpgme) otherwise break foreign target analysis of the reusable chart layers.

---

### 2.1 `MicroCanvas` & `Chart` (Drawing Layer)

#### The BTOP Multi-Resolution Canvas Engine (`canvas.go`)
Terminal character cells are treated as micro-grids with sub-pixel resolution:
- **Braille Mode (`U+2800..U+28FF`)**: 2 horizontal columns $\times$ 4 vertical rows = **8 sub-pixels per character cell**.
  - A 60-column $\times$ 10-row chart provides an effective **120 $\times$ 40 pixel drawing area**.
- **Block Mode (`quadrant blocks`)**: 2 horizontal columns $\times$ 2 vertical rows = **4 sub-pixels per character cell** (` `, `▗`, `▐`, `▖`, `▄`, `▟`, `▌`, `▙`, `█`).
- **TTY Mode (`shading / CP437`)**: Fallback using ` `, `░`, `▒`, `█` for raw Linux `/dev/tty` consoles or non-UTF-8 terminals.

#### Two rendering strategies, chosen by symbol mode

BTOP's $5 \times 5$ lookup tables encode **filled-from-bottom columns** (`left_level * 5 + right_level` → glyph). They cannot express an arbitrary line segment, and the TTY shading glyphs (` ░ ▒ █`) have no sub-cell geometry at all. So the canvas has two code paths:

| Mode    | Strategy | Primitive |
|---------|----------|-----------|
| braille | **Line** via per-dot bitmask | `setDot(x, y)` ORs one of the 8 Braille dot bits into the cell mask |
| block   | **Line** via per-dot bitmask | `setDot(x, y)` ORs one of the 4 quadrant bits; glyph = `quadrant[mask]` |
| tty     | **Fill** via level table  | column height quantised to 0..4, glyph from a 5-entry shading table; series drawn back-to-front, highest value first, so overlaps show the smaller series on top |

Braille dot bit layout (Unicode `U+2800` + mask):
```go
// column 0: bits 0,1,2,6 (rows 0..3 top→bottom); column 1: bits 3,4,5,7
var brailleDot = [2][4]rune{
    {0x01, 0x02, 0x04, 0x40},
    {0x08, 0x10, 0x20, 0x80},
}
// quadrant blocks indexed by mask (bit0=TL, bit1=TR, bit2=BL, bit3=BR)
var quadrant = [16]rune{' ', '▘', '▝', '▀', '▖', '▌', '▞', '▛', '▗', '▚', '▐', '▜', '▄', '▙', '▟', '█'}
var ttyFill = [5]rune{' ', '░', '▒', '▓', '█'}
```

The btop `brailleUp` fill table is kept only for the **legend sparklines**, which are filled single-series mini-graphs and read better as area than as a line.

#### Multi-Series Line Plotting & Color Resolution
- **Line Rendering (braille/block)**: Each series is rendered as a continuous curve across columns using sub-pixel points and Bresenham interpolation between consecutive samples. This prevents series from occluding one another.
- **Color Collision**:
  - In cells containing only one series, the cell is styled with that series' unique color.
  - In cells where multiple series intersect, the dot masks are combined with bitwise OR, and the foreground color is assigned to the series with the highest value in that column (or the focused series when the entity table has a selection).

#### `Chart` Primitive (`chart.go`)
A `tview.Box` implementation owning:
- `series map[string]*Series` + ordered slice for stable legend order.
- `unit`: `percent` (fixed 0–100 y-scale) or `bytes` (dynamic auto-scaling).
- **Smooth Auto-Scaling**: For memory/bytes mode, the y-axis max is computed from the **maximum value across the 60-second window** rather than the current frame, preventing axis jitter and bouncing.
- **Thread Safety**: Mutex guards ring buffer access. **No `QueueUpdateDraw`** — nothing in this repo uses it and dialogs have no `*tview.Application` reference. The collector pushes samples under the mutex; the app's 1 s refresh loop (`app/refresh.go`) calls `Draw()` on the same cadence as sampling, so the chart is repainted without extra plumbing. Hotkey changes (metric/symbol/pause) that should repaint immediately use the existing fast-refresh channel (`SetFastRefreshChannel`, as `ui/containers` already does).

---

### 2.2 `Collector` (Data Layer, Podman-Aware)

One collector instance runs per active dialog with a clean lifecycle:

#### 1. Container Stats Streaming (Low Overhead)
- **Do NOT poll one-shot HTTP calls per tick.** `containers.Stats` in Podman bindings is designed as an HTTP chunked stream (`Stream = true`).
- **Interval must be set explicitly.** The server defaults to one report every **5 s** when `StatsOptions.Interval` is unset (the existing container stats dialog has this bug). Pass `Interval = 1` to match the 1 s window.
- **Context must derive from the podman connection.** The bindings fetch the HTTP client from the context (`bindings.GetClient(ctx)`), so a context built from `context.Background()` fails with "no client". Correct form:
  ```go
  conn, err := registry.GetConnection()          // carries the client
  ctx, cancel := context.WithCancel(conn)
  ch, err := containers.Stats(ctx, ids, opts)    // via pdcs wrapper StatsStream
  ```
- It consumes the returned `chan entities.ContainerStatsReport` in a dedicated background reader goroutine.
- **Teardown — cancel AND drain.** After `cancel()`, the bindings goroutine's `dec.Decode` fails, and it sends **one final report with `Error != nil` on an unbuffered channel** before closing. If the reader has already exited, that send blocks forever and the goroutine leaks. The reader loop must therefore `range` over the channel until it is closed, ignoring reports after cancel; it must not `select` on a done-channel and return early. (The existing `ContainerStatsDialog` leaks exactly this way: it flips `*stream = false`, which does nothing server-side, and stops reading.)
- **Churn.** The stream is bound to the ID list given at open time. New containers are picked up by the 1 s `containers.List()` diff in the collector: when the set of running IDs changes, the collector cancels/drains the old stream and opens a new one. Ring buffers live in the collector, not the stream, so a restart loses no history.
- *pdcs wrapper:* `pdcs/containers.StatsStream(ctx context.Context, ids []string, intervalSec int) (chan entities.ContainerStatsReport, error)`. The caller owns `ctx`; the wrapper only adds logging and option construction.

#### 2. Host CPU & Memory
- **Local vs Remote Detection**: Read `registry.ConnectionURI()`.
  - **Local (`unix://`)**: Read `/sys/fs/cgroup/cpu.stat` (cgroup v2) or `/proc/stat` delta to produce an accurate host CPU series.
  - **Remote (`ssh://`, `tcp://`)**: Skip host cgroup reads. The system-scope "total" series is labeled `podman total` to clearly indicate it represents the sum of container workloads.
- **Host Memory Total**: `pdcs/sysinfo.SystemInfo` currently exposes only `MemUsagePC`; add a `MemTotal uint64` field populated from `response.Host.MemTotal` in `SysInfo()`. (There is no `system.Engine.GetSysUsage()` in this repo.) Used to compute memory % and handle unbounded containers (`MemLimit == 0`). Fetched once at dialog open, not per tick.
- **Local host CPU** lives in a new `pdcs/hoststats` package. It compiles on all targets (plain file reads) and simply returns "unavailable" on Windows/macOS, where podman-machine connections are `ssh://` anyway.

#### 3. Process Mode (`by-process`)
- Uses the existing `pdcs/containers.Top()` wrapper (the `/containers/{id}/top` API endpoint — no exec). Its parser already yields the `%CPU` column.
- Polling runs on a relaxed **3–5 second cycle** since it is one HTTP round-trip per container.
- Top processes are sorted by CPU; top $N$ ($N \approx 12$) become individual series, with remainder aggregated into `other`.
- *UI Note:* The UI clearly indicates process CPU is derived from `ps` (lifetime average) rather than instantaneous delta.

#### 4. Dynamic Entity Churn & Eviction
- Containers that stop or exit continue showing historical data until their 60-second buffer rolls over to zero.
- Once an inactive series has had 0/no data for 60 consecutive ticks and is no longer present in the podman listing, the collector **evicts the series** from memory to prevent leaks.
- **Infra containers**: at pod scope the stream includes the pod's infra container (`ListContainer.IsInfra`). It is excluded from `by-container` series by default (its load is ~0 and it clutters the legend) but still counted in the pod total.
- **Grouping by pod** at system scope uses `ListContainer.Pod` / `PodName` from `containers.List()`; containers with an empty `Pod` go into a `(no pod)` series.

---

### 2.3 `LoadChartDialog` (The Modal UI)

Follows the standard `podman-tui` dialog pattern ([`PodStatsDialog`](file:///home/damien/code/podman-tui/load_chart/ui/pods/poddialogs/stats.go#L37-L55)):

```
┌─ LOAD CHART ─ system · cpu% · braille · 60s · 1/s ───────────────────────┐
│ 100 ┤                                                    ⢀⣠⣴⣶⣾⣿   │
│  75 ┤                                           ⢀⣀⣤⣴⣶⣾⣿⠿⠛⠉       │
│  50 ┤                             ⢀⣀⣤⣴⣶⣾⣿⠿⠛⠉⠁                 │
│  25 ┤               ⣀⣀⣤⣴⣶⣾⣿⠿⠛⠉⠁                               │
│   0 ┼───────────────────────────────────────────────────────────────────┤
│     -60s           -45s           -30s           -15s          now      │
│  ● web       18.4%   ⣀⣀⣤⣴⣶⣾⣿⠿⠛⠉                                         │
│  ● db         4.2%   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀                                         │
│  ○ cache      (hidden)                                                  │
│ [g]roup: pod  [m]etric: cpu  [s]ymbol: braille  [t]oggle  [p]ause  [esc] │
└─────────────────────────────────────────────────────────────────────────┘
```

#### Dialog Controls
- **Dropdowns / Hotkeys**:
  - `g`: Cycle group-by mode (scope-aware)
  - `m`: Cycle metric (`CPU %` $\leftrightarrow$ `Memory MB/GB`)
  - `s`: Cycle rendering symbol (`braille` $\leftrightarrow$ `block` $\leftrightarrow$ `tty`)
  - `t`: Open entity toggle table
  - `p`: Pause/Resume chart scroll
  - `Esc`: Close dialog, cancel stream context, and release buffers

#### Constructor
```go
func NewLoadChartDialog(scope charts.Scope, resolve func() (charts.ScopeTarget, error)) *LoadChartDialog
```
*Guards (mirror the existing commands on each page):*
- **System page**: require a connected destination via `sys.destIsSet()`, exactly as `df()` does; otherwise return silently.
- **Pods page**: `selectedID == ""` → `displayError("", errNoPodSelected)`-style toast, as `stats()`/`top()` do.
- **Containers page**: `selectedID == ""` → error toast; additionally check `containers.Status(id) == "running"` as the existing `stats()` does, since a stopped container yields no stream.

In all three cases `resolve()` returns the error and the dialog is never displayed.

---

## 3. Integration Points

| File | Change |
| :--- | :--- |
| `ui/system/system.go` | Add `"load chart"` to `cmdDialog`, field `loadChartDialog`, inner dialog wiring. Scope = `ScopeSystem`. |
| `ui/pods/pods.go` | Add `"load chart"` to pods command menu. Scope = `ScopePod` (`selectedID`). |
| `ui/containers/containers.go` | Add `"load chart"` to containers command menu. Scope = `ScopeContainer` (`selectedID`). |
| `pdcs/containers/stats.go` | Add `StatsStream(ctx, ids []string, intervalSec int)`; caller supplies a context derived from `registry.GetConnection()`. Fix the existing `Stats()` path to set `Interval = 1` while here. |
| `pdcs/sysinfo/sysinfo.go` | Add `MemTotal uint64` to `SystemInfo`. |
| `pdcs/hoststats/` (new) | Host CPU reader (`/proc/stat` delta, cgroup v2 `cpu.stat`) for local `unix://` connections. |
| `ui/style/style.go` | Add the 12-entry chart palette (hex colours). |
| `app/app.go` | Pass the fast-refresh channel to the three pages' load chart dialogs (pods and system do not currently receive it; containers already does). |

---

## 4. Visual Quality Comparison

| Feature | lazydocker | Initial Rough Plan | Updated Plan (BTOP Engine) |
| :--- | :--- | :--- | :--- |
| **Canvas** | Stacked 1 chart per container | Multi-series on 1 canvas | Multi-series on 1 canvas |
| **Resolution** | Low (ASCII dots `*`) | Medium (1 sample/cell, 8 levels) | **High (1 sample/cell + interpolated midpoint, 4 vertical dots/cell Braille = 120x40 raster)** |
| **Fallbacks** | None | Fixed UTF-8 blocks | **3 selectable modes: Braille, Quadrant Block, TTY** |
| **Lines vs Area** | Single filled area | Overlapping block fills | **Clean multi-colored vector lines with dot combining** |
| **Legend** | Simple text name | Name + current value | **Color badge + Name + Humanized Value + Sparkline** |
| **Axis Scaling** | Fixed / Jittery | Frame max | **Windowed 60s max for smooth auto-scaling** |

---

## 5. Palette & Color Assignment (`colors.go`)

* **Assignment by first appearance, not by hash.** A pure `fnv1a(name) mod 12` collides often at realistic series counts (≈23 % chance of a clash with just 3 series, >50 % with 5). Instead the collector hands out the next unused palette slot when a series first appears, and remembers `name → slot` for the dialog's lifetime so toggling metric/symbol/group never recolours anything. Only when all 12 slots are taken does it fall back to `fnv1a(name) mod 12`.
* **Palette is defined as hex** via `tcell.NewHexColor`, in `ui/style`, because several of the originally listed names do not exist in tcell (`ColorAmber`, `ColorBrightGreen`, `ColorBrightBlue`). Twelve high-contrast values for dark backgrounds, e.g.: `#00D7FF` cyan, `#5FFF5F` green, `#FFAF00` orange, `#FF5FD7` magenta, `#5F87FF` blue, `#FFFF5F` yellow, `#FF87AF` pink, `#00AF87` teal, `#AFFF00` lime, `#AF87FF` violet, `#FFD700` amber, `#FF7F50` coral.
* The **Total / Aggregate** line always uses a dedicated fixed accent color (`style.FgColor` white) to stand out from individual series.

---

## 6. Build Order & Milestones

1. **Milestone 1 — MicroCanvas & Symbol Tables (`canvas.go`)**:
   - Braille/quadrant dot bitmasks + `setDot`, Bresenham line rasteriser with bitwise OR merging and per-cell colour resolution.
   - TTY fill path (5-level shading) and the btop `brailleUp` table for legend sparklines.
   - Unit tests with `tcell.NewSimulationScreen("UTF-8")` verifying glyph output; `//nolint:mnd` on the tables.
2. **Milestone 2 — Series & Ring Buffers (`series.go`, `chart.go`)**:
   - 60-sample circular buffer, windowed min/max, axis gridlines, and legend sparklines.
   - Palette slot assignment by first appearance.
3. **Milestone 3 — Streaming Collector (`collector.go`) + pdcs changes**:
   - `pdcs/containers.StatsStream`, `sysinfo.MemTotal`, `pdcs/hoststats`.
   - Wire the stream with a context derived from `registry.GetConnection()`, `Interval = 1`, and cancel-then-drain teardown.
   - Stream restart on container set change; local cgroup vs remote host CPU handling.
   - Container add/remove eviction logic; infra-container exclusion.
4. **Milestone 4 — Dialog Shell & Controls (`loadchart.go`)**:
   - `tview.Flex` layout, dropdown controls, hotkey handling (`g`, `m`, `s`, `t`, `p`, `Esc`) intercepted in the dialog's `InputHandler` before the focused dropdown sees them.
   - Fast-refresh channel wiring for immediate repaint on mode changes.
5. **Milestone 5 — Page Integrations**:
   - Wire into `ui/system/`, `ui/pods/`, and `ui/containers/`.
6. **Milestone 6 — Process Mode (`by-process`) & Polish**:
   - Top-N process polling, entity toggle modal, connection-loss banner overlay.

---

## 7. Testing Strategy

All tests follow the repo convention: **ginkgo/gomega** with a `charts_suite_test.go` entry point (see `ui/containers/cntdialogs/cntdialogs_suite_test.go`).

- **`ui/charts/canvas_test.go`**:
  - Validate `setDot` bit placement for every Braille and quadrant position, and the TTY level quantisation.
  - Test bitwise OR dot combination when two curves intersect at the same $(x, y)$ coordinate, and that the colour goes to the higher-valued series.
  - Bresenham between two samples produces a contiguous path (no gaps in any column).
- **`ui/charts/chart_test.go`**:
  - Run golden-master render tests using `tcell.NewSimulationScreen("UTF-8")`.
  - Concurrency test: execute simultaneous `Push()` calls from background goroutine during active `Draw()` calls under Go race detector (`go test -race`).
  - Palette: 12 distinct series get 12 distinct slots; re-adding a known name returns its original slot.
- **`ui/charts/collector_test.go`** (no podman needed — inject a fake `StatsStream` func that returns a channel the test controls):
  - After `cancel()`, the reader keeps draining until the channel is closed and the collector's goroutine count returns to baseline (`runtime.NumGoroutine` before/after, or a done-signal with timeout).
  - A final `Error != nil` report sent after cancel is swallowed, not surfaced to the UI.
  - Series eviction removes stale containers after 60 samples of zero/inactivity when absent from the listing.
  - Container set change triggers exactly one stream restart and preserves existing ring-buffer history.
