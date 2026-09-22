package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type BulkModelPatch struct {
	Enabled    *bool  `json:"enabled,omitempty"`
	TicketPlan string `json:"ticket_plan,omitempty"`
}

type ActionPayload struct {
	AccountID  int64                     `json:"account_id,omitempty"`
	Model      string                    `json:"model,omitempty"`
	AccountIDs []int64                   `json:"account_ids,omitempty"`
	Models     map[string]BulkModelPatch `json:"models,omitempty"`
}
type Action struct {
	Name     string        `json:"name"`
	ActionID string        `json:"action_id"`
	Payload  ActionPayload `json:"payload"`
}
type ActionResult struct {
	ActionID  string  `json:"action_id"`
	Name      string  `json:"name"`
	Accepted  bool    `json:"accepted"`
	Duplicate bool    `json:"duplicate"`
	Status    string  `json:"status"`
	Config    *Config `json:"config,omitempty"`
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
	if action.Name == "accounts.bulk_update" {
		return e.runBulkUpdate(action)
	}
	if action.Name == "accounts.remove" {
		return e.runRemoveAccount(action)
	}
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
		if !sameAction(record.Action, action) {
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

func (e *Engine) runBulkUpdate(action Action) (ActionResult, error) {
	if action.ActionID == "" || len(action.ActionID) > 256 {
		return ActionResult{}, errors.New("invalid action")
	}
	normalized, err := normalizeBulkAction(action)
	if err != nil {
		return ActionResult{}, err
	}
	action = normalized
	requestedIDs := append([]int64(nil), action.Payload.AccountIDs...)
	if len(requestedIDs) == 0 && action.Payload.AccountID > 0 {
		requestedIDs = append(requestedIDs, action.Payload.AccountID)
	}
	ids := make([]int64, 0, len(requestedIDs))
	seen := make(map[int64]bool, len(action.Payload.AccountIDs))
	for _, id := range requestedIDs {
		if id <= 0 || seen[id] {
			if id <= 0 {
				return ActionResult{}, errors.New("account_ids must be positive")
			}
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > 1000 || len(action.Payload.Models) == 0 {
		return ActionResult{}, errors.New("bulk update requires account_ids and models")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.bulkActions == nil {
		e.bulkActions = make(map[string]Action)
		e.bulkResults = make(map[string]ActionResult)
	}
	if previous, ok := e.bulkActions[action.ActionID]; ok {
		if !sameAction(previous, action) {
			return ActionResult{}, ErrActionConflict
		}
		result := e.bulkResults[action.ActionID]
		result.Duplicate = true
		return result, nil
	}
	accounts := make([]AccountConfig, len(e.current.config.Accounts))
	copy(accounts, e.current.config.Accounts)
	for index := range accounts {
		accounts[index].Models = copyModelConfig(accounts[index].Models)
	}
	for _, id := range ids {
		index := -1
		for i := range accounts {
			if accounts[i].AccountID == id {
				index = i
				break
			}
		}
		if index < 0 {
			accounts = append(accounts, AccountConfig{AccountID: id, Models: defaultModelConfig()})
			index = len(accounts) - 1
		}
		if accounts[index].Models == nil {
			accounts[index].Models = defaultModelConfig()
		}
		for model, patch := range action.Payload.Models {
			setting := accounts[index].Models[model]
			if setting.TicketPlan == "" {
				setting.TicketPlan = "pro"
			}
			if patch.Enabled != nil {
				setting.Enabled = *patch.Enabled
			}
			if patch.TicketPlan != "" {
				setting.TicketPlan = patch.TicketPlan
			}
			accounts[index].Models[model] = setting
		}
	}
	next := e.current.config
	next.Accounts = accounts
	public, err := validatedPublicConfig(next)
	if err != nil {
		return ActionResult{}, err
	}
	result := ActionResult{ActionID: action.ActionID, Name: action.Name, Accepted: true, Status: "prepared", Config: &public}
	e.bulkActions[action.ActionID] = action
	e.bulkResults[action.ActionID] = result
	return result, nil
}

func (e *Engine) runRemoveAccount(action Action) (ActionResult, error) {
	if action.ActionID == "" || len(action.ActionID) > 256 || action.Payload.AccountID <= 0 {
		return ActionResult{}, errors.New("invalid action")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if previous, ok := e.bulkActions[action.ActionID]; ok {
		if !sameAction(previous, action) {
			return ActionResult{}, ErrActionConflict
		}
		result := e.bulkResults[action.ActionID]
		result.Duplicate = true
		return result, nil
	}
	next := e.current.config
	found := false
	next.Accounts = make([]AccountConfig, 0, len(e.current.config.Accounts))
	for _, account := range e.current.config.Accounts {
		if account.AccountID != action.Payload.AccountID {
			next.Accounts = append(next.Accounts, account)
		} else {
			found = true
		}
	}
	public, err := validatedPublicConfig(next)
	if err != nil {
		return ActionResult{}, err
	}
	result := ActionResult{ActionID: action.ActionID, Name: action.Name, Accepted: true, Status: "prepared", Config: &public}
	if !found {
		result.Status = "not_found"
	}
	e.bulkActions[action.ActionID] = action
	e.bulkResults[action.ActionID] = result
	return result, nil
}

func normalizeBulkAction(action Action) (Action, error) {
	if action.Name != "accounts.bulk_update" {
		return Action{}, errors.New("invalid bulk action")
	}
	models := make(map[string]BulkModelPatch, len(action.Payload.Models))
	for model, patch := range action.Payload.Models {
		if !supportedModel(model) || (patch.Enabled == nil && patch.TicketPlan == "" && patch.TicketPlan != "__unchanged") {
			return Action{}, errors.New("invalid model patch")
		}
		if patch.TicketPlan == "__unchanged" {
			patch.TicketPlan = ""
		}
		if patch.Enabled == nil && patch.TicketPlan == "" {
			return Action{}, errors.New("invalid model patch")
		}
		if patch.TicketPlan != "" && patch.TicketPlan != "pro" && patch.TicketPlan != "team" {
			return Action{}, errors.New("ticket_plan must be pro or team")
		}
		models[model] = patch
	}
	action.Payload.Models = models
	return action, nil
}

func validatedPublicConfig(config Config) (Config, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return Config{}, err
	}
	validated, err := Validate(raw)
	if err != nil {
		return Config{}, err
	}
	validated.HarvestProxyURL = ""
	validated.DialProxyURL = ""
	return validated, nil
}

func sameAction(left, right Action) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return string(a) == string(b)
}

func copyModelConfig(input map[string]ModelConfig) map[string]ModelConfig {
	output := make(map[string]ModelConfig, len(input)+len(Models))
	for model, setting := range input {
		output[model] = setting
	}
	return output
}

func defaultModelConfig() map[string]ModelConfig {
	output := make(map[string]ModelConfig, len(Models))
	for _, model := range Models {
		output[model] = ModelConfig{TicketPlan: "pro"}
	}
	return output
}
