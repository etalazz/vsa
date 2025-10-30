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

// VEnvFileSink appends Vector envelopes to a JSONL log for audit/replay.
type VEnvFileSink struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	path string

	lastFlush time.Time
}

func NewVEnvFileSink(path string) (*VEnvFileSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &VEnvFileSink{f: f, w: bufio.NewWriterSize(f, 1<<20), path: path, lastFlush: time.Now()}, nil
}

func (s *VEnvFileSink) Append(env tfd.Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.w)
	_ = enc.Encode(&env)
	if time.Since(s.lastFlush) > 100*time.Millisecond {
		_ = s.w.Flush()
		s.lastFlush = time.Now()
	}
}

func (s *VEnvFileSink) AppendAll(envs []tfd.Envelope) {
	if len(envs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.w)
	for i := range envs {
		_ = enc.Encode(&envs[i])
	}
	if time.Since(s.lastFlush) > 100*time.Millisecond {
		_ = s.w.Flush()
		s.lastFlush = time.Now()
	}
}

func (s *VEnvFileSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFlush = time.Now()
	return s.w.Flush()
}

func (s *VEnvFileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.w.Flush()
	return s.f.Close()
}

// ReadAllVLog reads the Vector envelope log for replay.
func ReadAllVLog(path string) ([]tfd.Envelope, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []tfd.Envelope
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1<<20)
	scanner.Buffer(buf, 1<<26)
	for scanner.Scan() {
		var e tfd.Envelope
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
			out = append(out, e)
		}
	}
	return out, scanner.Err()
}
