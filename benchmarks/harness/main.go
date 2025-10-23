// Copyright 2025 Esteban Alvarez. All Rights Reserved.
//
// Created: October 2025
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"vsa"
)

type variantType string

const (
	variantVSA    variantType = "vsa"
	variantAtomic variantType = "atomic"
	variantBatch  variantType = "batch"
	variantCRDT   variantType = "crdt"
	variantToken  variantType = "token"
	variantLeaky  variantType = "leaky"
)

type metrics struct {
	latencies []time.Duration
	longOps   int64 // ops slower than 5x median
}

type persister struct {
	writeDelay    time.Duration
	logicalWrites atomic.Int64
	dbCalls       atomic.Int64
}

func newPersister(delay time.Duration) *persister { return &persister{writeDelay: delay} }

// write simulates a datastore write call that records n logical events in one db call.
func (p *persister) write(n int) {
	// Count a DB call even when n == 0 (e.g., CRDT merge control-plane)
	p.dbCalls.Add(1)
	if n > 0 {
		p.logicalWrites.Add(int64(n))
	}
	if p.writeDelay > 0 {
		time.Sleep(p.writeDelay)
	}
}

// ---- Producers (hot path) implement the same interface ----

type producer interface {
	update(key string, delta int64) // hot-path update (measured)
	startBG()
	stopBG()
}

// ---- Atomic variant (persist every op) ----

type atomicCounter struct {
	p *persister
	m sync.Map // key -> *atomic.Int64 (we only need presence; value unused)
}

func newAtomic(p *persister) *atomicCounter { return &atomicCounter{p: p} }

func (a *atomicCounter) update(key string, delta int64) {
	// Simulate an immediate persist for each logical op.
	a.p.write(1)
}
func (a *atomicCounter) startBG() {}
func (a *atomicCounter) stopBG()  {}

// ---- Batching variant (group ops by size or time; still logicalWrites = N) ----

type batcher struct {
	p         *persister
	batchSize int
	interval  time.Duration

	mu    sync.Mutex
	buf   int
	stopC chan struct{}
	wg    sync.WaitGroup
}

func newBatcher(p *persister, size int, interval time.Duration) *batcher {
	return &batcher{p: p, batchSize: size, interval: interval, stopC: make(chan struct{})}
}

func (b *batcher) update(_ string, delta int64) {
	// Every logical op is still a logical write; we only reduce dbCalls via batching.
	b.mu.Lock()
	b.buf++
	if b.buf >= b.batchSize {
		n := b.buf
		b.buf = 0
		b.mu.Unlock()
		b.p.write(n)
		return
	}
	b.mu.Unlock()
}

func (b *batcher) startBG() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		t := time.NewTicker(b.interval)
		defer t.Stop()
		for {
			select {
			case <-b.stopC:
				return
			case <-t.C:
				b.mu.Lock()
				n := b.buf
				b.buf = 0
				b.mu.Unlock()
				if n > 0 {
					b.p.write(n)
				}
			}
		}
	}()
}

func (b *batcher) stopBG() {
	close(b.stopC)
	b.wg.Wait()
	b.mu.Lock()
	n := b.buf
	b.buf = 0
	b.mu.Unlock()
	if n > 0 {
		b.p.write(n)
	}
}

// ---- VSA variant (net-based persist) ----

type vsaHarness struct {
	p         *persister
	threshold int64
	interval  time.Duration
	maxAge    time.Duration
	keys      []string

	lowThreshold int64 // hysteresis low watermark (≈ threshold/2)

	store sync.Map // key -> *vsa.VSA
	// last touch per key (UnixNano)
	lastTouch sync.Map // key -> int64
	// hysteresis arm per key: true means eligible to commit when high threshold hit
	hysteresisArm sync.Map // key -> *atomic.Bool

	// metrics
	maxAbsVec   atomic.Int64
	sumAbsVec   atomic.Int64
	samples     atomic.Int64
	finalAbsVec atomic.Int64

	// commit interval metrics (ns)
	lastCommitTS atomic.Int64 // last commit time
	minCommitNS  atomic.Int64
	maxCommitNS  atomic.Int64
	sumCommitNS  atomic.Int64
	commitCount  atomic.Int64 // intervals counted (needs >=2 commits)
	totalCommits atomic.Int64 // total commits applied (>=1 means lastCommitTS valid)

	stopC chan struct{}
	wg    sync.WaitGroup
}

func newVSAHarness(p *persister, keys []string, initialScalar int64, threshold int64, interval time.Duration) *vsaHarness {
	vh := &vsaHarness{p: p, threshold: threshold, interval: interval, keys: keys, stopC: make(chan struct{})}
	vh.lowThreshold = max64(1, threshold/2)
	for _, k := range keys {
		vh.store.Store(k, vsa.New(initialScalar))
		vh.lastTouch.Store(k, time.Now().UnixNano())
		b := &atomic.Bool{}
		b.Store(true) // armed initially
		vh.hysteresisArm.Store(k, b)
	}
	vh.minCommitNS.Store(0)
	vh.maxCommitNS.Store(0)
	return vh
}

func (v *vsaHarness) update(key string, delta int64) {
	if val, ok := v.store.Load(key); ok {
		val.(*vsa.VSA).Update(delta)
		v.lastTouch.Store(key, time.Now().UnixNano())
	}
}

func (v *vsaHarness) startBG() {
	// Commit/flush loop
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		t := time.NewTicker(v.interval)
		defer t.Stop()
		for {
			select {
			case <-v.stopC:
				// final flush: commit any non-zero vectors and record final |A_net|
				var finalTotal int64
				v.store.Range(func(key, value any) bool {
					vs := value.(*vsa.VSA)
					_, vec := vs.State()
					if vec != 0 {
						v.p.write(1) // one net write
						vs.Commit(vec)
					}
					if vec < 0 {
						vec = -vec
					}
					finalTotal += vec
					return true
				})
				v.finalAbsVec.Store(finalTotal)
				return
			case <-t.C:
				now := time.Now()
				var commits []struct {
					vs  *vsa.VSA
					vec int64
					key string
				}
				v.store.Range(func(key, value any) bool {
					vs := value.(*vsa.VSA)
					ok, vec := vs.CheckCommit(v.threshold)
					// Hysteresis arming check
					armAny, _ := v.hysteresisArm.Load(key)
					armed := true
					if armAny != nil {
						armed = armAny.(*atomic.Bool).Load()
					}
					if ok && armed {
						commits = append(commits, struct {
							vs  *vsa.VSA
							vec int64
							key string
						}{vs, vec, key.(string)})
					} else {
						// Re-arm when below low threshold
						absVec := vec
						if absVec == 0 {
							_, absVec = vs.State()
						}
						if absVec < 0 {
							absVec = -absVec
						}
						if absVec <= v.lowThreshold {
							if armAny == nil {
								b := &atomic.Bool{}
								b.Store(true)
								v.hysteresisArm.Store(key, b)
							} else {
								armAny.(*atomic.Bool).Store(true)
							}
						}
					}
					// Max-age flush (independent of arming)
					if !ok && v.maxAge > 0 {
						ltRaw, _ := v.lastTouch.Load(key)
						if ltRaw != nil {
							lt := time.Unix(0, ltRaw.(int64))
							if vec == 0 {
								_, vec = vs.State()
							}
							if vec != 0 && now.Sub(lt) >= v.maxAge {
								commits = append(commits, struct {
									vs  *vsa.VSA
									vec int64
									key string
								}{vs, vec, key.(string)})
							}
						}
					}
					return true
				})
				if len(commits) > 0 {
					// record commit interval metrics
					ns := time.Now().UnixNano()
					prev := v.lastCommitTS.Swap(ns)
					if prev != 0 {
						interval := ns - prev
						// update min/max/sum atomically (best-effort)
						for {
							old := v.minCommitNS.Load()
							if old == 0 || interval < old {
								if v.minCommitNS.CompareAndSwap(old, interval) {
									break
								}
							} else {
								break
							}
						}
						for {
							old := v.maxCommitNS.Load()
							if interval > old {
								if v.maxCommitNS.CompareAndSwap(old, interval) {
									break
								}
							} else {
								break
							}
						}
						v.sumCommitNS.Add(interval)
						v.commitCount.Add(1)
					}
					// one db call per key (simple model); each is a net logical write (1)
					for range commits {
						v.p.write(1)
					}
					for _, c := range commits {
						c.vs.Commit(c.vec)
						// disarm hysteresis for committed keys
						if armAny, _ := v.hysteresisArm.Load(c.key); armAny != nil {
							armAny.(*atomic.Bool).Store(false)
						}
					}
					// record total commits applied (for telemetry when <2 commits)
					v.totalCommits.Add(int64(len(commits)))
				}
			}
		}
	}()

	// Sampler loop: track total |A_net| over time for max/mean
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		s := time.NewTicker(v.interval)
		defer s.Stop()
		for {
			select {
			case <-v.stopC:
				return
			case <-s.C:
				var total int64
				v.store.Range(func(key, value any) bool {
					_, vec := value.(*vsa.VSA).State()
					if vec < 0 {
						vec = -vec
					}
					total += vec
					return true
				})
				v.sumAbsVec.Add(total)
				v.samples.Add(1)
				for {
					old := v.maxAbsVec.Load()
					if total > old {
						if v.maxAbsVec.CompareAndSwap(old, total) {
							break
						}
					} else {
						break
					}
				}
			}
		}
	}()
}

func (v *vsaHarness) stopBG() {
	close(v.stopC)
	v.wg.Wait()
}

// ---- CRDT PN-counter (minimal simulation) ----

type pnCounter struct {
	p        *persister
	replicas int
	interval time.Duration
	keys     []string

	// per key → per replica (P,N)
	mu   sync.Mutex
	pos  map[string][]int64
	neg  map[string][]int64
	stop chan struct{}
	wg   sync.WaitGroup
}

func newPN(p *persister, keys []string, replicas int, interval time.Duration) *pnCounter {
	mPos := make(map[string][]int64, len(keys))
	mNeg := make(map[string][]int64, len(keys))
	for _, k := range keys {
		mPos[k] = make([]int64, replicas)
		mNeg[k] = make([]int64, replicas)
	}
	return &pnCounter{p: p, replicas: replicas, interval: interval, keys: keys, pos: mPos, neg: mNeg, stop: make(chan struct{})}
}

func (c *pnCounter) update(key string, delta int64) {
	// Pick a replica by hash for determinism
	r := int(fnv32(key)) % c.replicas
	c.mu.Lock()
	if delta >= 0 {
		c.pos[key][r] += delta
	} else {
		c.neg[key][r] += -delta
	}
	c.mu.Unlock()
	// Local write per event (logical and db call)
	c.p.write(1)
}

func (c *pnCounter) startBG() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(c.interval)
		defer t.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-t.C:
				// Simulate merge: one db call per replica (lightweight)
				for range c.replicas {
					c.p.write(0) // count dbCalls only (no new logical events)
				}
			}
		}
	}()
}

func (c *pnCounter) stopBG() {
	close(c.stop)
	c.wg.Wait()
}

// ---- Token Bucket (baseline) ----

type tokenBucket struct {
	p      *persister
	cap    float64
	rate   float64 // tokens per second
	mu     sync.Mutex
	tokens map[string]float64
	last   map[string]time.Time
}

func newTokenBucket(p *persister, keys []string, capacity int, rate float64) *tokenBucket {
	tb := &tokenBucket{p: p, cap: float64(capacity), rate: rate, tokens: make(map[string]float64, len(keys)), last: make(map[string]time.Time, len(keys))}
	now := time.Now()
	for _, k := range keys {
		tb.tokens[k] = float64(capacity)
		tb.last[k] = now
	}
	return tb
}

func (t *tokenBucket) update(key string, delta int64) {
	// Refill and consume/refund locally; simulate a read + a write to external store per op
	t.mu.Lock()
	now := time.Now()
	if prev, ok := t.last[key]; ok {
		refill := now.Sub(prev).Seconds() * t.rate
		if refill > 0 {
			t.tokens[key] += refill
			if t.tokens[key] > t.cap {
				t.tokens[key] = t.cap
			}
		}
	} else {
		// initialize on first touch
		t.tokens[key] = t.cap
	}
	t.last[key] = now
	if delta >= 0 {
		if t.tokens[key] >= 1 {
			t.tokens[key] -= 1
		}
	} else {
		// refund/add a token back (bounded by cap)
		t.tokens[key] += 1
		if t.tokens[key] > t.cap {
			t.tokens[key] = t.cap
		}
	}
	t.mu.Unlock()
	// Simulate one read + one write per logical operation
	t.p.write(0) // read
	t.p.write(1) // write
}
func (t *tokenBucket) startBG() {}
func (t *tokenBucket) stopBG()  {}

// ---- Leaky Bucket (baseline) ----

type leakyBucket struct {
	p        *persister
	rate     float64 // leak rate per second
	capacity float64
	mu       sync.Mutex
	level    map[string]float64
	last     map[string]time.Time
}

func newLeakyBucket(p *persister, keys []string, capacity int, rate float64) *leakyBucket {
	lb := &leakyBucket{p: p, rate: rate, capacity: float64(capacity), level: make(map[string]float64, len(keys)), last: make(map[string]time.Time, len(keys))}
	now := time.Now()
	for _, k := range keys {
		lb.level[k] = 0
		lb.last[k] = now
	}
	return lb
}

func (l *leakyBucket) update(key string, delta int64) {
	// Apply leak and enqueue/dequeue; simulate read + write per op
	l.mu.Lock()
	now := time.Now()
	prev := l.last[key]
	leaked := now.Sub(prev).Seconds() * l.rate
	if leaked > 0 {
		l.level[key] -= leaked
		if l.level[key] < 0 {
			l.level[key] = 0
		}
	}
	l.last[key] = now
	if delta >= 0 {
		// add one unit if capacity allows
		if l.level[key] < l.capacity {
			l.level[key] += 1
		}
	} else {
		// negative deltas reduce queued level
		l.level[key] -= 1
		if l.level[key] < 0 {
			l.level[key] = 0
		}
	}
	l.mu.Unlock()
	l.p.write(0) // read
	l.p.write(1) // write
}
func (l *leakyBucket) startBG() {}
func (l *leakyBucket) stopBG()  {}

// ---- Runner ----

func main() {
	var (
		variantStr = flag.String("variant", "vsa", "vsa|atomic|batch|crdt|token|leaky")
		opCount    = flag.Int("ops", 200_000, "total operations across all goroutines")
		workers    = flag.Int("goroutines", 32, "concurrent workers")
		keysN      = flag.Int("keys", 1, "number of hot keys")
		churnPct   = flag.Int("churn", 50, "percentage of negative ops [0..100]")
		seed       = flag.Int64("seed", 1, "PRNG seed")

		// VSA
		threshold      = flag.Int64("threshold", 64, "VSA commit threshold")
		lowThreshold   = flag.Int64("low_threshold", 0, "VSA hysteresis low watermark; if 0, defaults to threshold/2")
		commitInterval = flag.Duration("commit_interval", 10*time.Millisecond, "VSA commit scan interval")
		commitMaxAge   = flag.Duration("commit_max_age", 15*time.Millisecond, "VSA max-age flush; commit even if below threshold when no changes for this duration (0 to disable)")
		initialScalar  = flag.Int64("initial_scalar", 1_000_000, "initial scalar per key (not persisted)")

		// Batching
		batchSize     = flag.Int("batch_size", 64, "batch size")
		batchInterval = flag.Duration("batch_interval", 10*time.Millisecond, "batch flush interval")

		// CRDT
		replicas    = flag.Int("replicas", 4, "CRDT replicas")
		mergePeriod = flag.Duration("merge_interval", 25*time.Millisecond, "CRDT merge interval")

		// Baselines (token/leaky)
		rate  = flag.Float64("rate", 10000, "rate tokens/sec for token/leaky baselines")
		burst = flag.Int("burst", 100, "capacity/burst for token/leaky baselines")

		// Persistence
		writeDelay = flag.Duration("write_delay", 0, "simulated delay per datastore call (e.g., 50us, 1ms)")

		// Harness
		pprofOn       = flag.Bool("pprof", false, "enable pprof on localhost:6060")
		sampleEvery   = flag.Int("sample_every", 1, "record latency every N ops (1=all)")
		maxLatSamples = flag.Int("max_latency_samples", 200000, "cap on stored latency samples to bound memory; downsample if exceeded")
		duration      = flag.Duration("duration", 0, "run for this duration instead of a fixed -ops (0 to disable)")
	)
	flag.Parse()

	if *pprofOn {
		go func() { _ = http.ListenAndServe("localhost:6060", nil) }()
	}

	v := variantType(strings.ToLower(*variantStr))
	if v != variantVSA && v != variantAtomic && v != variantBatch && v != variantCRDT && v != variantToken && v != variantLeaky {
		fmt.Println("-variant must be one of: vsa|atomic|batch|crdt|token|leaky")
		os.Exit(2)
	}

	keys := make([]string, *keysN)
	for i := 0; i < *keysN; i++ {
		keys[i] = fmt.Sprintf("key-%d", i)
	}
	p := newPersister(*writeDelay)

	var prod producer
	switch v {
	case variantAtomic:
		prod = newAtomic(p)
	case variantBatch:
		prod = newBatcher(p, *batchSize, *batchInterval)
	case variantCRDT:
		prod = newPN(p, keys, *replicas, *mergePeriod)
	case variantToken:
		prod = newTokenBucket(p, keys, *burst, *rate)
	case variantLeaky:
		prod = newLeakyBucket(p, keys, *burst, *rate)
	case variantVSA:
		prod = newVSAHarness(p, keys, *initialScalar, *threshold, *commitInterval)
		// set max-age flush and hysteresis low watermark on VSA harness if provided
		if vh, ok := prod.(*vsaHarness); ok {
			vh.maxAge = *commitMaxAge
			if *lowThreshold > 0 {
				vh.lowThreshold = *lowThreshold
			}
		}
	}

	prod.startBG()
	defer prod.stopBG()

	// Pre-generate ops to avoid per-op RNG and allocations
	m := &metrics{latencies: make([]time.Duration, 0, *opCount)}
	opsPerWorker := *opCount / *workers
	if *duration > 0 {
		// For duration-based runs, pre-generate a small fixed slice and cycle over it
		opsPerWorker = 8192
	}
	opsKeys := make([][]string, *workers)
	opsDelta := make([][]int64, *workers)
	for g := 0; g < *workers; g++ {
		rnd := rand.New(rand.NewPCG(uint64(*seed), uint64(g)+1))
		ks := make([]string, opsPerWorker)
		ds := make([]int64, opsPerWorker)
		for i := 0; i < opsPerWorker; i++ {
			ks[i] = keys[rnd.IntN(len(keys))]
			if rnd.IntN(100) < *churnPct {
				ds[i] = -1
			} else {
				ds[i] = 1
			}
		}
		opsKeys[g] = ks
		opsDelta[g] = ds
	}

	// Run workers
	var wg sync.WaitGroup
	wg.Add(*workers)
	start := time.Now()
	// Duration-based mode if -duration > 0
	durationMode := *duration > 0
	deadline := time.Time{}
	if durationMode {
		deadline = start.Add(*duration)
	}
	var opsDone atomic.Int64

	recordLatency := *maxLatSamples != 0

	latSlices := make([][]time.Duration, *workers)
	// Cap per-worker latency storage in duration mode using reservoir sampling
	capPerWorker := 0
	if recordLatency && *maxLatSamples > 0 {
		capPerWorker = *maxLatSamples / *workers
		if capPerWorker < 1 {
			capPerWorker = 1
		}
	}
	for g := 0; g < *workers; g++ {
		go func(id int) {
			defer wg.Done()
			ks := opsKeys[id]
			ds := opsDelta[id]
			// preallocate sampled latencies for this worker if recording is enabled
			sample := *sampleEvery
			if sample <= 0 {
				sample = 1
			}
			var loc []time.Duration
			if recordLatency {
				if durationMode && capPerWorker > 0 {
					loc = make([]time.Duration, 0, capPerWorker)
				} else {
					loc = make([]time.Duration, 0, (len(ks)+sample-1)/sample)
				}
			}
			// rng for reservoir sampling
			var rndLoc *rand.Rand
			if durationMode && recordLatency && capPerWorker > 0 {
				rndLoc = rand.New(rand.NewPCG(uint64(*seed), uint64(id)+12345))
			}
			totalSeen := 0
			if durationMode {
				// Run until deadline; cycle over pre-generated ops to avoid allocs
				for i := 0; ; i++ {
					if time.Now().After(deadline) {
						break
					}
					idx := i % len(ks)
					if recordLatency && (sample == 1 || (i%sample) == 0) {
						t0 := time.Now()
						prod.update(ks[idx], ds[idx])
						d := time.Since(t0)
						if capPerWorker > 0 {
							totalSeen++
							if totalSeen <= capPerWorker {
								loc = append(loc, d)
							} else {
								j := rndLoc.IntN(totalSeen)
								if j < capPerWorker {
									loc[j] = d
								}
							}
						} else {
							loc = append(loc, d)
						}
					} else {
						prod.update(ks[idx], ds[idx])
					}
					opsDone.Add(1)
				}
			} else {
				for i := 0; i < len(ks); i++ {
					if recordLatency && (sample == 1 || (i%sample) == 0) {
						t0 := time.Now()
						prod.update(ks[i], ds[i])
						loc = append(loc, time.Since(t0))
					} else {
						prod.update(ks[i], ds[i])
					}
					opsDone.Add(1)
				}
			}
			latSlices[id] = loc
		}(g)
	}
	wg.Wait()

	// Merge sampled latencies
	for i, ls := range latSlices {
		m.latencies = append(m.latencies, ls...)
		latSlices[i] = nil // free per-worker slice
	}
	// Downsample if exceeding cap to bound memory
	if *maxLatSamples > 0 && len(m.latencies) > *maxLatSamples {
		capN := *maxLatSamples
		reduced := make([]time.Duration, capN)
		step := float64(len(m.latencies)) / float64(capN)
		for j := 0; j < capN; j++ {
			idx := int(float64(j) * step)
			if idx >= len(m.latencies) {
				idx = len(m.latencies) - 1
			}
			reduced[j] = m.latencies[idx]
		}
		m.latencies = reduced
	}
	// Free pre-generated ops to reduce live memory footprint before stats
	opsKeys = nil
	opsDelta = nil

	runDur := time.Since(start)

	// allow background to catch up a tick
	time.Sleep(2 * time.Millisecond)

	// stats
	// Sort latencies once to compute quantiles without extra allocations
	sort.Slice(m.latencies, func(i, j int) bool { return m.latencies[i] < m.latencies[j] })
	idx50 := (len(m.latencies) - 1) * 50 / 100
	idx95 := (len(m.latencies) - 1) * 95 / 100
	idx99 := (len(m.latencies) - 1) * 99 / 100
	p50 := time.Duration(0)
	p95 := time.Duration(0)
	p99 := time.Duration(0)
	if len(m.latencies) > 0 {
		p50 = m.latencies[idx50]
		p95 = m.latencies[idx95]
		p99 = m.latencies[idx99]
	}
	med := p50
	thr := 5 * med
	for _, d := range m.latencies {
		if d > thr {
			m.longOps++
		}
	}
	// build latency histogram (ns/us/ms buckets)
	hist := buildLatencyHistogram(m.latencies)

	// Release latency samples before taking memory snapshot to reduce live Alloc
	m.latencies = nil
	// Encourage a GC so snapshot reflects released buffers
	runtime.GC()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	actualOps := opsDone.Load()
	fmt.Printf("Variant: %s  Ops: %d  Goroutines: %d  Keys: %d  Churn: %d%%\n", v, actualOps, *workers, *keysN, *churnPct)
	fmt.Printf("Duration: %s  Ops/sec: %s\n", runDur.Round(time.Millisecond), humanRate(float64(actualOps)/runDur.Seconds()))
	// Print latencies with adaptive precision to avoid clamped zeros
	fmt.Printf("Latency p50: %sµs  p95: %sµs  p99: %sµs\n", formatMicros(med), formatMicros(p95), formatMicros(p99))
	fmt.Println("Latency histogram (non-zero buckets):")
	for _, b := range hist {
		fmt.Printf("  %s: %d\n", b.label, b.count)
	}
	fmt.Printf("Writes: logical=%s (%s/sec), dbCalls=%s (%s/sec)\n",
		humanInt(p.logicalWrites.Load()), humanRate(float64(p.logicalWrites.Load())/runDur.Seconds()),
		humanInt(p.dbCalls.Load()), humanRate(float64(p.dbCalls.Load())/runDur.Seconds()))
	fmt.Printf("Memory: Alloc=%s  TotalAlloc=%s  Sys=%s  NumGC=%d\n",
		humanBytes(ms.Alloc), humanBytes(ms.TotalAlloc), humanBytes(ms.Sys), ms.NumGC)
	fmt.Printf("Contention (long ops >5× median): %d\n", m.longOps)

	// Machine-readable one-line summary for scripts
	fmt.Printf("Summary: variant=%s ops=%d duration_ns=%d goroutines=%d keys=%d churn_pct=%d p50_ns=%d p95_ns=%d p99_ns=%d logical_writes=%d db_calls=%d write_delay_ns=%d\n",
		v, actualOps, runDur.Nanoseconds(), *workers, *keysN, *churnPct, int64(med), int64(p95), int64(p99), p.logicalWrites.Load(), p.dbCalls.Load(), int64(p.writeDelay))

	// VSA-specific metrics
	if v == variantVSA {
		if vh, ok := prod.(*vsaHarness); ok {
			avg := int64(0)
			s := vh.samples.Load()
			if s > 0 {
				avg = vh.sumAbsVec.Load() / s
			}
			fmt.Printf("VSA |A_net| total: max=%s avg=%s final=%s (units)\n",
				humanInt(vh.maxAbsVec.Load()), humanInt(avg), humanInt(vh.finalAbsVec.Load()))
			total := vh.totalCommits.Load()
			cc := vh.commitCount.Load()
			if total >= 2 && cc > 0 {
				avgNS := vh.sumCommitNS.Load() / cc
				fmt.Printf("VSA commits: total=%d | intervals min=%s avg=%s max=%s\n",
					total, time.Duration(vh.minCommitNS.Load()), time.Duration(avgNS), time.Duration(vh.maxCommitNS.Load()))
			} else if total >= 1 {
				last := vh.lastCommitTS.Load()
				if last > 0 {
					age := time.Since(time.Unix(0, last))
					fmt.Printf("VSA commits: total=%d | last age=%s\n", total, age)
				} else {
					fmt.Printf("VSA commits: total=%d\n", total)
				}
			} else {
				fmt.Println("VSA commits: none")
			}
		}
	}
}

// ---- Helpers ----

type histBucket struct {
	label  string
	lo, hi time.Duration
	count  int64
}

func buildLatencyHistogram(durations []time.Duration) []histBucket {
	b := []histBucket{
		{"<100ns", 0, 100 * time.Nanosecond, 0},
		{"100–200ns", 100 * time.Nanosecond, 200 * time.Nanosecond, 0},
		{"200–500ns", 200 * time.Nanosecond, 500 * time.Nanosecond, 0},
		{"0.5–1µs", 500 * time.Nanosecond, 1 * time.Microsecond, 0},
		{"1–2µs", 1 * time.Microsecond, 2 * time.Microsecond, 0},
		{"2–5µs", 2 * time.Microsecond, 5 * time.Microsecond, 0},
		{"5–10µs", 5 * time.Microsecond, 10 * time.Microsecond, 0},
		{"10–20µs", 10 * time.Microsecond, 20 * time.Microsecond, 0},
		{"20–50µs", 20 * time.Microsecond, 50 * time.Microsecond, 0},
		{"50–100µs", 50 * time.Microsecond, 100 * time.Microsecond, 0},
		{"0.1–0.2ms", 100 * time.Microsecond, 200 * time.Microsecond, 0},
		{"0.2–0.5ms", 200 * time.Microsecond, 500 * time.Microsecond, 0},
		{"0.5–1ms", 500 * time.Microsecond, 1 * time.Millisecond, 0},
		{"1–2ms", 1 * time.Millisecond, 2 * time.Millisecond, 0},
		{"2–5ms", 2 * time.Millisecond, 5 * time.Millisecond, 0},
		{"5–10ms", 5 * time.Millisecond, 10 * time.Millisecond, 0},
		{">=10ms", 10 * time.Millisecond, time.Duration(1<<63 - 1), 0},
	}
	for _, d := range durations {
		for i := range b {
			if d >= b[i].lo && d < b[i].hi {
				b[i].count++
				break
			}
		}
	}
	// Return only non-zero buckets
	out := make([]histBucket, 0, len(b))
	for _, x := range b {
		if x.count > 0 {
			out = append(out, x)
		}
	}
	return out
}

func percentiles(durations []time.Duration, p int) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	copyArr := make([]time.Duration, len(durations))
	copy(copyArr, durations)
	sort.Slice(copyArr, func(i, j int) bool { return copyArr[i] < copyArr[j] })
	idx := (len(copyArr) - 1) * p / 100
	return copyArr[idx]
}

// formatMicros returns a string with microseconds value using adaptive precision
// to avoid clamped zeros for sub-microsecond durations.
func formatMicros(d time.Duration) string {
	us := float64(d) / 1e3 // d is ns
	if us < 1 {
		return fmt.Sprintf("%.3f", us)
	}
	if us < 100 {
		return fmt.Sprintf("%.1f", us)
	}
	return fmt.Sprintf("%.0f", us)
}

func humanInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg = "-"
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i != 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return neg + string(out)
}

func humanRate(x float64) string {
	if x >= 1_000_000 {
		return fmt.Sprintf("%.1fM", x/1_000_000)
	}
	if x >= 1_000 {
		return fmt.Sprintf("%.1fk", x/1_000)
	}
	return fmt.Sprintf("%.0f", x)
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	d := float64(b)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	i := 0
	for d >= unit && i < len(units)-1 {
		d /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", d, units[i])
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// simple FNV-1a 32-bit for stable hashing
func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
