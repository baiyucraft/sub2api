package service

import (
	"container/heap"
	"time"
)

type openAITTFTGuardAccountKey struct{ accountID, upstreamKeyID int64 }

type openAITTFTGuardExpiryKey struct {
	key         openAITTFTGuardKey
	observation bool
}

type openAITTFTGuardExpiryItem struct {
	key          openAITTFTGuardExpiryKey
	at, sampleAt time.Time
	generation   uint64
	index        int
}

type openAITTFTGuardExpiryHeap []*openAITTFTGuardExpiryItem

func (h openAITTFTGuardExpiryHeap) Len() int           { return len(h) }
func (h openAITTFTGuardExpiryHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h openAITTFTGuardExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *openAITTFTGuardExpiryHeap) Push(x any) {
	p := x.(*openAITTFTGuardExpiryItem)
	p.index = len(*h)
	*h = append(*h, p)
}
func (h *openAITTFTGuardExpiryHeap) Pop() any {
	a := *h
	n := len(a)
	p := a[n-1]
	a[n-1] = nil
	p.index = -1
	*h = a[:n-1]
	return p
}

func (g *openAITTFTGuard) scheduleExpiryLocked(key openAITTFTGuardKey, observation bool, e *openAITTFTGuardEntry) {
	k := openAITTFTGuardExpiryKey{key: key, observation: observation}
	age := g.ttl
	if observation {
		age *= 2
	}
	if item := g.expiryItems[k]; item != nil {
		item.at, item.sampleAt, item.generation = e.lastSampleAt.Add(age), e.lastSampleAt, e.generation
		heap.Fix(&g.expiries, item.index)
	} else {
		item := &openAITTFTGuardExpiryItem{key: k, at: e.lastSampleAt.Add(age), sampleAt: e.lastSampleAt, generation: e.generation}
		g.expiryItems[k] = item
		heap.Push(&g.expiries, item)
	}
	g.recalculateNextExpiryLocked()
}

func (g *openAITTFTGuard) removeExpiryLocked(key openAITTFTGuardKey, observation bool) {
	k := openAITTFTGuardExpiryKey{key: key, observation: observation}
	if item := g.expiryItems[k]; item != nil {
		heap.Remove(&g.expiries, item.index)
		delete(g.expiryItems, k)
	}
	g.recalculateNextExpiryLocked()
}

func (g *openAITTFTGuard) indexObservationLocked(k openAITTFTGuardKey) {
	identity := openAITTFTGuardAccountKey{k.accountID, k.upstreamKeyID}
	models := g.observationModels[identity]
	if models == nil {
		models = map[openAITTFTGuardKey]struct{}{}
		g.observationModels[identity] = models
	}
	models[k] = struct{}{}
	g.observationAccounts[k.accountID]++
}

func (g *openAITTFTGuard) indexProjectionLocked(k openAITTFTGuardKey) {
	raw := k
	raw.scopeID = 0
	keys := g.projectionKeys[raw]
	if keys == nil {
		keys = map[openAITTFTGuardKey]struct{}{}
		g.projectionKeys[raw] = keys
	}
	keys[k] = struct{}{}
}

func (g *openAITTFTGuard) deleteProjectionLocked(k openAITTFTGuardKey) {
	delete(g.entries, k)
	g.removeExpiryLocked(k, false)
	raw := k
	raw.scopeID = 0
	delete(g.projectionKeys[raw], k)
	if len(g.projectionKeys[raw]) == 0 {
		delete(g.projectionKeys, raw)
	}
}

func (g *openAITTFTGuard) deleteObservationLocked(k openAITTFTGuardKey) {
	delete(g.observations, k)
	g.removeExpiryLocked(k, true)
	identity := openAITTFTGuardAccountKey{k.accountID, k.upstreamKeyID}
	delete(g.observationModels[identity], k)
	if len(g.observationModels[identity]) == 0 {
		delete(g.observationModels, identity)
	}
	g.observationAccounts[k.accountID]--
	if g.observationAccounts[k.accountID] == 0 {
		delete(g.observationAccounts, k.accountID)
		delete(g.accountKeys, k.accountID)
	}
}

func (g *openAITTFTGuard) deleteExpiredLocked(now time.Time) {
	for len(g.expiries) > 0 && !g.expiries[0].at.After(now) {
		item := heap.Pop(&g.expiries).(*openAITTFTGuardExpiryItem)
		delete(g.expiryItems, item.key)
		e := g.entries[item.key.key]
		if item.key.observation {
			e = g.observations[item.key.key]
		}
		if e == nil || !e.lastSampleAt.Equal(item.sampleAt) || e.generation != item.generation {
			continue
		}
		if item.key.observation {
			g.deleteObservationLocked(item.key.key)
		} else {
			g.deleteProjectionLocked(item.key.key)
		}
	}
	g.recalculateNextExpiryLocked()
}

func (g *openAITTFTGuard) recountActivePoliciesLocked() {
	g.activePolicies = 0
	for _, cfg := range g.groupConfigs {
		if cfg.Enabled {
			g.activePolicies++
		}
	}
}
