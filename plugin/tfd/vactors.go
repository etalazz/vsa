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

package tfd

import (
	"container/list"
)

// VActor represents a per-key ordered queue with an audit chain.
type VActor struct {
	keyID uint64
	prev  [16]byte
	queue *list.List // of Envelope
}

func newVActor(keyID uint64) *VActor {
	return &VActor{keyID: keyID, queue: list.New()}
}

// Enqueue appends a V-envelope and updates the prev-hash link.
func (a *VActor) Enqueue(env Envelope) {
	if env.Channel != ChannelVector {
		return
	}
	// compute prev-hash as a function of key and last seqEnd
	a.prev = Hash128(a.keyID, env.SeqEnd)
	env.HashPrev = a.prev
	a.queue.PushBack(env)
}

// Drain returns all queued envelopes in order and clears the queue.
func (a *VActor) Drain() []Envelope {
	var out []Envelope
	for e := a.queue.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(Envelope))
	}
	a.queue.Init()
	return out
}

// VRouter sharded map from keyID to actor.
type VRouter struct {
	actors map[uint64]*VActor
}

func NewVRouter() *VRouter {
	return &VRouter{actors: make(map[uint64]*VActor)}
}

func (r *VRouter) Route(keyID uint64) *VActor {
	act := r.actors[keyID]
	if act == nil {
		act = newVActor(keyID)
		r.actors[keyID] = act
	}
	return act
}
