package churn

import (
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type point struct {
	ts      time.Time
	naive   int64
	commits int64
	sumAbs  int64
	sumNet  int64
}

// Internal aggregates and exporter loop

type keyAgg struct {
	abs        atomic.Int64 // sum of absolute updates (admitted requests)
	net        atomic.Int64 // sum of absolute committed vectors
	lastUpdate atomic.Int64 // unix nano
}

var (
	agg sync.Map // map[uint64]*keyAgg

	naiveWritesInternal atomic.Int64 // sampled naive admits (for per-key churn/top-N)
	naiveWritesAll      atomic.Int64 // unsampled naive admits (global baseline for write-reduction)
	commitRowsInternal  atomic.Int64 // global committed rows across batches
	// Global sampled sums for churn KPI (independent of map iteration)
	sumAbsGlobal atomic.Int64 // sum of abs updates for sampled keys (since start)
	sumNetGlobal atomic.Int64 // sum of abs(net commits) for sampled keys (since start)

	exporterMu   sync.Mutex
	exporterStop chan struct{}
	exporterDone chan struct{}
	currCfg      atomic.Value // stores Config

	// rolling window points for KPIs (protected by windowMu)
	windowPoints []point
	windowMu     sync.Mutex

	livePrinted   atomic.Bool
	liveMode      atomic.Bool
	ansiSupported atomic.Bool
	colorOn       atomic.Bool

	prevSimpleLen atomic.Int64
)

func startOrUpdateExporter(cfg Config) {
	exporterMu.Lock()
	defer exporterMu.Unlock()

	currCfg.Store(cfg)

	// configure live mode and colors (env overrides allowed)
	lm := os.Getenv("VSA_CHURN_LIVE")
	if lm == "0" || lm == "false" { // opt-out
		liveMode.Store(false)
	} else {
		liveMode.Store(true)
	}
	if os.Getenv("NO_COLOR") != "" {
		colorOn.Store(false)
	} else {
		colorOn.Store(true)
	}
	// detect ANSI support or choose simple renderer
	ansiSupported.Store(detectANSISupport())

	// Stop previous loop if running
	if exporterStop != nil {
		close(exporterStop)
		<-exporterDone
		exporterStop, exporterDone = nil, nil
	}
	if !cfg.Enabled || cfg.LogInterval <= 0 {
		return
	}
	// Start new loop
	exporterStop = make(chan struct{})
	exporterDone = make(chan struct{})
	go exporterLoop(exporterStop, exporterDone)
}

func exporterLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	cfgAny := currCfg.Load()
	cfg, _ := cfgAny.(Config)
	// cfg.LogInterval is guaranteed > 0 by the starter
	ticker := time.NewTicker(cfg.LogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			publishSnapshot()
		case <-stop:
			return
		}
	}
}

func publishSnapshot() {
	// Load current config snapshot safely
	cfgAny := currCfg.Load()
	cfg, _ := cfgAny.(Config)
	// Snapshot aggregates and evict idle keys beyond 2x Window
	type row struct {
		keyHash     uint64
		abs, net    int64
		churnFactor float64
	}
	rows := make([]row, 0, 1024)
	var tracked int
	idleTTL := cfg.Window * 2
	cutoff := time.Now().Add(-idleTTL).UnixNano()
	agg.Range(func(k, v any) bool {
		ka := v.(*keyAgg)
		last := ka.lastUpdate.Load()
		if last > 0 && last < cutoff {
			agg.Delete(k)
			return true
		}
		tracked++
		a := ka.abs.Load()
		n := ka.net.Load()
		cf := float64(a) / float64(max64(1, abs64(n)))
		rows = append(rows, row{keyHash: k.(uint64), abs: a, net: n, churnFactor: cf})
		return true
	})
	keysTracked.Set(float64(tracked))

	// Pick TopN by churnFactor then by abs desc
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].churnFactor == rows[j].churnFactor {
			return rows[i].abs > rows[j].abs
		}
		return rows[i].churnFactor > rows[j].churnFactor
	})
	if len(rows) > cfg.TopN {
		rows = rows[:cfg.TopN]
	}

	// Windowed KPIs using rolling points
	now := time.Now()
	pt := point{
		ts:      now,
		naive:   naiveWritesAll.Load(),
		commits: commitRowsInternal.Load(),
		sumAbs:  sumAbsGlobal.Load(),
		sumNet:  sumNetGlobal.Load(),
	}
	// Protect windowPoints against concurrent publisher/test calls
	windowMu.Lock()
	windowPoints = append(windowPoints, pt)
	// prune old
	winStart := now.Add(-cfg.Window)
	idx := 0
	for idx < len(windowPoints) && windowPoints[idx].ts.Before(winStart) {
		idx++
	}
	if idx > 0 {
		windowPoints = windowPoints[idx:]
	}
	old := windowPoints[0]
	windowMu.Unlock()

	dNaive := pt.naive - old.naive
	dCommits := pt.commits - old.commits
	dAbs := pt.sumAbs - old.sumAbs
	dNet := pt.sumNet - old.sumNet
	wrWindow := 1.0 - float64(dCommits)/float64(max64(1, dNaive))
	churnWin := float64(dAbs) / float64(max64(1, abs64(dNet)))
	// Set KPI gauges
	writeReductionRatio.Set(wrWindow)
	churnRatio.Set(churnWin)

	// Build summary and one top-key line (windowed numbers)
	wrTxt := fmt.Sprintf("%.3f", wrWindow)
	cfTxt := fmt.Sprintf("%.3f", churnWin)
	if colorOn.Load() {
		wrTxt = colorWR(wrWindow, wrTxt)
		cfTxt = colorCF(churnWin, cfTxt)
	}
	summary := fmt.Sprintf("churn summary: global_churn=%s write_reduction_est=%s naive=%d commits=%d sample=%.2f topN=%d",
		cfTxt, wrTxt, dNaive, dCommits, cfg.SampleRate, cfg.TopN)

	var topLine string
	if len(rows) > 0 {
		first := rows[0]
		churnTxt := fmt.Sprintf("%.3f", first.churnFactor)
		if colorOn.Load() {
			churnTxt = colorCF(first.churnFactor, churnTxt)
		}
		topLine = fmt.Sprintf("top key=%s churn=%s abs=%d net=%d",
			shortHash(first.keyHash, cfg.KeyHashLen), churnTxt, first.abs, first.net)
	} else {
		topLine = "top key: (none yet)"
	}

	if liveMode.Load() {
		if ansiSupported.Load() {
			renderLive(summary, topLine)
		} else {
			renderSimple(summary, topLine)
		}
		return
	}

	// Fallback: print multi-line snapshot (previous behavior)
	ts := time.Now().Format(time.RFC3339)
	fmt.Printf("[%s] %s\n", ts, summary)
	fmt.Printf("  - %s\n", topLine)
}

func shortHash(h uint64, n int) string {
	if n <= 0 {
		n = 8
	}
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[7-i] = byte(h & 0xff)
		h >>= 8
	}
	s := hex.EncodeToString(b) // 16 hex chars
	if n < len(s) {
		return s[:n]
	}
	return s
}

func sampleRate() float64 {
	thr := samplingThreshold.Load()
	return float64(thr) / float64(^uint64(0))
}

// --- recording helpers (called from prom_counters.go) ---

func exporterRecordAdmit(keyHash uint64) {
	ka := getAgg(keyHash)
	ka.abs.Add(1)
	ka.lastUpdate.Store(time.Now().UnixNano())
	naiveWritesInternal.Add(1)
	// Update global sampled abs sum for churn KPI
	sumAbsGlobal.Add(1)
}

func exporterRecordCommit(keyHash uint64, vector int64) {
	ka := getAgg(keyHash)
	v := vector
	if v < 0 {
		v = -v
	}
	ka.net.Add(v)
	ka.lastUpdate.Store(time.Now().UnixNano())
	// Update global sampled net sum for churn KPI
	sumNetGlobal.Add(v)
}

func getAgg(keyHash uint64) *keyAgg {
	if v, ok := agg.Load(keyHash); ok {
		return v.(*keyAgg)
	}
	ka := &keyAgg{}
	actual, _ := agg.LoadOrStore(keyHash, ka)
	return actual.(*keyAgg)
}

// ObserveBatch is already exported in prom_counters.go, but we additionally
// maintain internal counters for write_reduction estimate here.
func exporterObserveBatchInternal(size int) {
	if size > 0 {
		commitRowsInternal.Add(int64(size))
	}
}

// Utilities
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// --- Live rendering and coloring helpers ---

const (
	ansiClearLine  = "\x1b[2K"
	ansiPrevLines2 = "\x1b[2F" // move cursor to beginning of the line, 2 lines up
	ansiReset      = "\x1b[0m"
	ansiBold       = "\x1b[1m"
	ansiRed        = "\x1b[31m"
	ansiGreen      = "\x1b[32m"
	ansiYellow     = "\x1b[33m"
	ansiCyan       = "\x1b[36m"
)

func renderLive(summary, top string) {
	// First time: just print two lines
	if !livePrinted.Load() {
		fmt.Printf("%s\n%s\n", summary, top)
		livePrinted.Store(true)
		return
	}
	// Move up two lines and overwrite both
	fmt.Print(ansiPrevLines2)
	fmt.Printf("%s%s\n", ansiClearLine, summary)
	fmt.Printf("%s%s\n", ansiClearLine, top)
}

// renderSimple overwrites a single line using carriage return so consoles without
// ANSI cursor movement (e.g., some IDE run consoles) don't spam new lines.
// It prints the summary plus a compact top-key tail on the same line.
func renderSimple(summary, top string) {
	line := summary
	if top != "" && top != "top key: (none yet)" {
		line = line + " | " + top
	}
	visLen := printableLen(line)
	prev := prevSimpleLen.Load()
	// First render: print without newline
	if !livePrinted.Load() {
		fmt.Print(line)
		livePrinted.Store(true)
		prevSimpleLen.Store(int64(visLen))
		return
	}
	// Overwrite in place
	pad := int(prev) - visLen
	if pad < 0 {
		pad = 0
	}
	if pad > 0 {
		fmt.Printf("\r%s%s", line, strings.Repeat(" ", pad))
	} else {
		fmt.Printf("\r%s", line)
	}
	prevSimpleLen.Store(int64(visLen))
}

// printableLen returns the visible character length after stripping ANSI escapes.
func printableLen(s string) int {
	// Fast path: if no ESC, return raw length
	if !strings.Contains(s, "\x1b") {
		return len(s)
	}
	b := make([]byte, 0, len(s))
	inEsc := false
	csi := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inEsc {
			// For CSI sequences (ESC '[' ... <final>), ignore everything until the final byte (0x40..0x7E)
			if !csi {
				if c == '[' { // start of CSI
					csi = true
					continue
				}
				// Non-CSI escape: consume until a final byte, then end
				if c >= 0x40 && c <= 0x7E {
					inEsc = false
					csi = false
				}
				continue
			}
			// Inside CSI: keep skipping until we reach the final byte (letters like 'm', 'K', etc.)
			if c >= 0x40 && c <= 0x7E {
				inEsc = false
				csi = false
			}
			continue
		}
		if c == 0x1b { // ESC
			inEsc = true
			csi = false
			continue
		}
		b = append(b, c)
	}
	return len(b)
}

// detectANSISupport best-effort heuristic for cursor movement capability.
func detectANSISupport() bool {
	// Respect explicit opt-out via env used earlier by liveMode
	if os.Getenv("VSA_CHURN_LIVE") == "0" || strings.EqualFold(os.Getenv("VSA_CHURN_LIVE"), "false") {
		return false
	}
	// JetBrains consoles often support colors but not cursor movements reliably
	if os.Getenv("GOLAND_IDE") != "" || os.Getenv("IDEA_INITIAL_DIRECTORY") != "" {
		return false
	}
	term := strings.ToLower(os.Getenv("TERM"))
	if runtime.GOOS == "windows" {
		// Windows Terminal/ConEmu typically OK; plain PowerShell/GoLand often not.
		if os.Getenv("WT_SESSION") != "" || strings.EqualFold(os.Getenv("ConEmuANSI"), "ON") {
			return true
		}
		// Some shells set TERM to xterm-256color when they support ANSI
		return strings.Contains(term, "xterm") || strings.Contains(term, "ansi")
	}
	// On Unix, require a TERM that usually supports cursor movement
	if term == "" {
		return false
	}
	return strings.Contains(term, "xterm") || strings.Contains(term, "screen") || strings.Contains(term, "tmux") || strings.Contains(term, "ansi")
}

func colorWR(val float64, txt string) string {
	if !colorOn.Load() {
		return txt
	}
	switch {
	case val >= 0.95:
		return ansiBold + ansiGreen + txt + ansiReset
	case val >= 0.80:
		return ansiYellow + txt + ansiReset
	default:
		return ansiRed + txt + ansiReset
	}
}

func colorCF(val float64, txt string) string {
	if !colorOn.Load() {
		return txt
	}
	// Higher churn implies more coalescing opportunity; emphasize with cyan, fade otherwise
	switch {
	case val >= 3.0:
		return ansiBold + ansiCyan + txt + ansiReset
	case val >= 1.5:
		return ansiCyan + txt + ansiReset
	default:
		return txt
	}
}
