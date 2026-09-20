package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type ActionPayload struct {
	AccountID int64  `json:"account_id"`
	Model     string `json:"model"`
}
type Action struct {
	Name     string        `json:"name"`
	ActionID string        `json:"action_id"`
	Payload  ActionPayload `json:"payload"`
}
type ActionResult struct {
	ActionID  string `json:"action_id"`
	Name      string `json:"name"`
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	Status    string `json:"status"`
}
type actionRecord struct {
	Action Action       `json:"action"`
	Result ActionResult `json:"result"`
	Done   bool         `json:"done"`
	Guard  Guard        `json:"guard"`
}

// RunAction journals a global action_id and a per-slot applied marker. A crash
// between the two CAS writes can safely replay the pending journal: it cannot
// restart a finished harvest or recancel a later request. IDs are never evicted.
func (e *Engine) RunAction(ctx context.Context, action Action) (ActionResult, error) {
	if (action.Name != "harvest" && action.Name != "cancel") || action.ActionID == "" || len(action.ActionID) > 256 || action.Payload.AccountID <= 0 || !supportedModel(action.Payload.Model) {
		return ActionResult{}, errors.New("invalid action")
	}
	s := e.snap()
	g, _, err := e.guard(ctx, s, action.Payload.AccountID, action.Payload.Model)
	if err != nil {
		return ActionResult{}, err
	}
	digest := sha256.Sum256([]byte(action.ActionID))
	hash := hex.EncodeToString(digest[:])
	key := "actions/" + hash
	lease, err := e.host.LeaseAcquire(ctx, LeaseRequest{key, e.opts.Owner + ":" + randomID(), e.opts.CleanupTimeout, g})
	if err != nil {
		return ActionResult{}, err
	}
	defer e.release(lease)
	stored, err := e.host.StateGet(ctx, key, g)
	if err != nil {
		return ActionResult{}, err
	}
	record := actionRecord{Action: action, Guard: g, Result: ActionResult{ActionID: action.ActionID, Name: action.Name, Accepted: true, Status: "queued"}}
	if action.Name == "cancel" {
		record.Result.Status = "cancelled"
	}
	duplicate := len(stored.Data) > 0
	if duplicate {
		if json.Unmarshal(stored.Data, &record) != nil {
			return ActionResult{}, ErrUnavailable
		}
		if record.Action != action {
			return ActionResult{}, ErrActionConflict
		}
		if record.Done {
			result := record.Result
			result.Duplicate = true
			return result, nil
		}
		if record.Guard != g {
			return ActionResult{}, ErrStale
		}
	} else {
		data, _ := json.Marshal(record)
		stored.Version, err = e.host.StateCAS(ctx, Mutation{key, stored.Version, data, g, lease})
		if err != nil {
			return ActionResult{}, err
		}
	}
	_, err = e.mutate(ctx, s, g, nil, func(slot *Slot) (bool, error) {
		if slot.Actions[hash] {
			return false, nil
		}
		if slot.Actions == nil {
			slot.Actions = map[string]bool{}
		}
		slot.Actions[hash] = true
		if action.Name == "cancel" {
			slot.JobEpoch++
			slot.Harvesting = false
			slot.HarvestRequested = false
			slot.Paused = true
		} else {
			slot.Paused = false
			slot.HarvestRequested = true
		}
		return true, nil
	})
	if err != nil {
		return ActionResult{}, err
	}
	if action.Name == "cancel" {
		e.mu.Lock()
		if j := e.jobs[slotKey(g.AccountID, g.Model)]; j != nil {
			j.cancel()
		}
		e.mu.Unlock()
	}
	record.Done = true
	data, _ := json.Marshal(record)
	_, err = e.host.StateCAS(ctx, Mutation{key, stored.Version, data, g, lease})
	if err != nil {
		return ActionResult{}, err
	}
	record.Result.Duplicate = duplicate
	// Once the durable journal says Done, replay no longer needs the per-slot
	// marker. Keep slots bounded during normal operation; failed cleanup is safe.
	_, _ = e.mutate(ctx, s, g, nil, func(slot *Slot) (bool, error) {
		if !slot.Actions[hash] {
			return false, nil
		}
		delete(slot.Actions, hash)
		return true, nil
	})
	return record.Result, nil
}
