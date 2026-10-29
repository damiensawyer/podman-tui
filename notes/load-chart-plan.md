# Load Chart — Architecture & Implementation Plan

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
- **History**: 60 samples window at 1 sample/s (matches `utils.RefreshInterval`). In Braille mode (2 points per cell), 60 columns display up to 120 micro-samples.

---

## 2. Architecture — Three Shared Layers

The new package lives under `ui/charts/`. The canvas and primitive layers contain **zero podman-specific dependencies** so they can be reused for infobar sparklines, dashboard widgets, or daemon monitors.

```
ui/charts/
  canvas.go      MicroCanvas: 2x4 sub-cell grid, BTOP lookup tables, line rasterizer
  chart.go       Chart primitive: canvas + N series + ring buffers + axes + legend
  series.go      Series model: ID, name, color, 60-sample ring buffer, window stats
  collector.go   Background collector: persistent streaming stats, cgroup/host stats
  loadchart.go   LoadChartDialog: tview dialog shell, hotkeys, control dropdowns
  colors.go      Stable FNV-1a palette hash -> 12 high-contrast tcell colors
```

---

### 2.1 `MicroCanvas` & `Chart` (Drawing Layer)

#### The BTOP Multi-Resolution Canvas Engine (`canvas.go`)
Terminal character cells are treated as micro-grids with sub-pixel resolution:
- **Braille Mode (`U+2800..U+28FF`)**: 2 horizontal columns $\times$ 4 vertical rows = **8 sub-pixels per character cell**.
  - A 60-column $\times$ 10-row chart provides an effective **120 $\times$ 40 pixel drawing area**.
- **Block Mode (`quadrant blocks`)**: 2 horizontal columns $\times$ 2 vertical rows = **4 sub-pixels per character cell** (` `, `▗`, `▐`, `▖`, `▄`, `▟`, `▌`, `▙`, `█`).
- **TTY Mode (`shading / CP437`)**: Fallback using ` `, `░`, `▒`, `█` for raw Linux `/dev/tty` consoles or non-UTF-8 terminals.

BTOP's fast $5 \times 5$ lookup matrix translates `(left_level * 5 + right_level)` into glyphs:
```go
// 5 levels (0=empty, 1..4=height of dots/sub-blocks)
var brailleUp = [25]rune{
    ' ', '⢀', '⢠', '⢰', '⢸',
    '⡀', '⣀', '⣠', '⣰', '⣸',
    '⡄', '⣄', '⣤', '⣴', '⣼',
    '⡆', '⣆', '⣦', '⣶', '⣾',
    '⡇', '⣇', '⣧', '⣷', '⣿',
}
```

#### Multi-Series Line Plotting & Color Resolution
- **Line Rendering (Not Filled Area)**: Each series is rendered as a continuous curve across columns (using sub-pixel points and Bresenham interpolation between samples). This prevents series from occluding one another.
- **Color Collision**:
  - In cells containing only one series, the cell is styled with that series' unique color.
  - In cells where multiple series intersect, the Braille dot patterns are combined using bitwise OR (`rune = 0x2800 | maskA | maskB`), and the foreground color is assigned to the active or highest-value series.

#### `Chart` Primitive (`chart.go`)
A `tview.Box` implementation owning:
- `series map[string]*Series` + ordered slice for stable legend order.
- `unit`: `percent` (fixed 0–100 y-scale) or `bytes` (dynamic auto-scaling).
- **Smooth Auto-Scaling**: For memory/bytes mode, the y-axis max is computed from the **maximum value across the 60-second window** rather than the current frame, preventing axis jitter and bouncing.
- **Thread Safety**: Mutex guards ring buffer access. UI redraws are dispatched through `app.QueueUpdateDraw()`.

---

### 2.2 `Collector` (Data Layer, Podman-Aware)

One collector instance runs per active dialog with a clean lifecycle:

#### 1. Container Stats Streaming (Low Overhead)
- **Do NOT poll one-shot HTTP calls per tick.** `containers.Stats` in Podman bindings is designed as an HTTP chunked stream (`Stream = true`).
- The collector creates a cancellable context (`ctx, cancel := context.WithCancel(context.Background())`) and invokes `containers.Stats(ctx, containerIDs, opts)`.
- It consumes the returned `chan entities.ContainerStatsReport` in a dedicated background reader goroutine.
- **Teardown**: Calling `cancel()` cleanly terminates the Podman server-side streaming session and closes channels without goroutine leaks.
- *Note:* Add a helper in `pdcs/containers` to wrap `containers.Stats` taking a slice of container IDs (`[]string`).

#### 2. Host CPU & Memory
- **Local vs Remote Detection**: Read `registry.ConnectionURI()`.
  - **Local (`unix://`)**: Read `/sys/fs/cgroup/cpu.stat` (cgroup v2) or `/proc/stat` delta to produce an accurate host CPU series.
  - **Remote (`ssh://`, `tcp://`)**: Skip host cgroup reads. The system-scope "total" series is labeled `podman total` to clearly indicate it represents the sum of container workloads.
- **Host Memory Total**: Reused from `system.Engine.GetSysUsage()` or `sysinfo.SysInfo()` (`Host.MemTotal`) to compute memory % and handle unbounded containers (`MemLimit == 0`).

#### 3. Process Mode (`by-process`)
- Top polling runs on a relaxed **3–5 second cycle** to minimize exec overhead inside container namespaces.
- Top processes are sorted by CPU; top $N$ ($N \approx 12$) become individual series, with remainder aggregated into `other`.
- *UI Note:* The UI clearly indicates process CPU is derived from `ps` (lifetime average) rather than instantaneous delta.

#### 4. Dynamic Entity Churn & Eviction
- Containers that stop or exit continue showing historical data until their 60-second buffer rolls over to zero.
- Once an inactive series has had 0/no data for 60 consecutive ticks and is no longer present in the podman listing, the collector **evicts the series** from memory to prevent leaks.

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
*Guard:* If on the Pods or Containers page and no item is selected, `resolve()` returns an error and the page displays the standard error toast (`displayError("", errNoSelectedContainer)`) rather than opening an uninitialized dialog.

---

## 3. Integration Points

| File | Change |
| :--- | :--- |
| `ui/system/system.go` | Add `"load chart"` to `cmdDialog`, field `loadChartDialog`, inner dialog wiring. Scope = `ScopeSystem`. |
| `ui/pods/pods.go` | Add `"load chart"` to pods command menu. Scope = `ScopePod` (`selectedID`). |
| `ui/containers/containers.go` | Add `"load chart"` to containers command menu. Scope = `ScopeContainer` (`selectedID`). |
| `pdcs/containers/stats.go` | Add multi-container streaming stats function: `StatsList(ctx context.Context, ids []string)`. |
| `pdcs/system/hoststats.go` | Add host CPU cgroup parser for local Unix socket connections. |
| `ui/style/style.go` | Add color palette definitions for multi-series graphs. |

---

## 4. Visual Quality Comparison

| Feature | lazydocker | Initial Rough Plan | Updated Plan (BTOP Engine) |
| :--- | :--- | :--- | :--- |
| **Canvas** | Stacked 1 chart per container | Multi-series on 1 canvas | Multi-series on 1 canvas |
| **Resolution** | Low (ASCII dots `*`) | Medium (1 sample/cell, 8 levels) | **High (2 samples/cell, 8 levels Braille = 120x40)** |
| **Fallbacks** | None | Fixed UTF-8 blocks | **3 selectable modes: Braille, Quadrant Block, TTY** |
| **Lines vs Area** | Single filled area | Overlapping block fills | **Clean multi-colored vector lines with dot combining** |
| **Legend** | Simple text name | Name + current value | **Color badge + Name + Humanized Value + Sparkline** |
| **Axis Scaling** | Fixed / Jittery | Frame max | **Windowed 60s max for smooth auto-scaling** |

---

## 5. Palette & Color Assignment (`colors.go`)

To ensure consistent identification across sessions and mode toggles:
* Entity names are hashed using **FNV-1a**:
  $$\text{index} = \text{fnv1a}(\text{name}) \pmod{12}$$
* Maps into a high-contrast palette of 12 distinct `tcell` colors chosen for readability on dark backgrounds (Cyan, BrightGreen, Orange, Magenta, BrightBlue, Yellow, Pink, Teal, Lime, Violet, Amber, Coral).
* The **Total / Aggregate** line always uses a dedicated fixed accent color (`tcell.ColorWhite` or `tcell.ColorYellow`) to stand out from individual series.

---

## 6. Build Order & Milestones

1. **Milestone 1 — MicroCanvas & Symbol Tables (`canvas.go`)**:
   - Implement BTOP's 5x5 lookup matrices for Braille, Block, and TTY modes.
   - Implement line drawing with bitwise OR dot-merging and cell styling.
   - Unit tests with mock `tcell.SimulationScreen` verifying glyph output.
2. **Milestone 2 — Series & Ring Buffers (`series.go`, `chart.go`)**:
   - 60-sample circular buffer, windowed min/max, axis gridlines, and legend sparklines.
3. **Milestone 3 — Streaming Collector (`collector.go`)**:
   - Wire `containers.Stats` stream with cancellable context.
   - Local cgroup vs remote host CPU handling.
   - Container add/remove eviction logic.
4. **Milestone 4 — Dialog Shell & Controls (`loadchart.go`)**:
   - `tview.Flex` layout, dropdown controls, hotkey handling (`g`, `m`, `s`, `t`, `p`, `Esc`).
5. **Milestone 5 — Page Integrations**:
   - Wire into `ui/system/`, `ui/pods/`, and `ui/containers/`.
6. **Milestone 6 — Process Mode (`by-process`) & Polish**:
   - Top-N process polling, entity toggle modal, connection-loss banner overlay.

---

## 7. Testing Strategy

- **`ui/charts/canvas_test.go`**:
  - Validate lookup matrix generation for `braille_up`, `block_up`, `tty_up`.
  - Test bitwise OR dot combination when two curves intersect at the same $(x, y)$ coordinate.
- **`ui/charts/chart_test.go`**:
  - Run golden-master render tests using `tcell.NewSimulationScreen("UTF-8")`.
  - Concurrency test: execute simultaneous `Push()` calls from background goroutine during active `Draw()` calls under Go race detector (`go test -race`).
- **`ui/charts/collector_test.go`**:
  - Verify cancellation context cleanly terminates stats reader loop.
  - Verify series eviction removes stale containers after 60 samples of zero/inactivity.
