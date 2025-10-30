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

package sinks

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"

	tfd "vsa/plugin/tfd"
)

// SBatchFileSink is a buffered JSONL sink for S-batches. It is safe for
// concurrent use and optimized for append-only workloads.
type SBatchFileSink struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	path string

	lastFlush time.Time
}

// NewSBatchFileSink opens (or creates) the file at path in append mode with
// a buffered writer. Call Close() when done.
func NewSBatchFileSink(path string) (*SBatchFileSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s := &SBatchFileSink{f: f, w: bufio.NewWriterSize(f, 1<<20 /*1MiB*/), path: path, lastFlush: time.Now()}
	return s, nil
}

// OnSBatches writes the batches as JSON lines.
func (s *SBatchFileSink) OnSBatches(b []tfd.SBatch) {
	if len(b) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.w)
	for _, sb := range b {
		if err := enc.Encode(&sb); err != nil {
			// best effort: on error, try to flush and retry once
			_ = s.w.Flush()
			_ = enc.Encode(&sb)
		}
	}
	// Flush periodically to bound data loss on crash and for visibility in /state.
	if time.Since(s.lastFlush) > 100*time.Millisecond {
		_ = s.w.Flush()
		s.lastFlush = time.Now()
	}
}

// Flush forces buffered data to be written to disk.
func (s *SBatchFileSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFlush = time.Now()
	return s.w.Flush()
}

// Close flushes and closes the underlying file.
func (s *SBatchFileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.w.Flush()
	return s.f.Close()
}

// ReadAllSLog reads the entire S-batch log file as a slice. Intended for demo/replay.
func ReadAllSLog(path string) ([]tfd.SBatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []tfd.SBatch
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1<<20)
	scanner.Buffer(buf, 1<<26)
	for scanner.Scan() {
		var sb tfd.SBatch
		if err := json.Unmarshal(scanner.Bytes(), &sb); err == nil {
			out = append(out, sb)
		}
	}
	return out, scanner.Err()
}
