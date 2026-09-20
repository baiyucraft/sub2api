package core

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func (e *Engine) supervise(s snapshot) {
	ticker := time.NewTicker(e.opts.PollInterval)
	defer ticker.Stop()
	for {
		for _, a := range s.config.Accounts {
			for _, model := range Models {
				if !e.live(s) {
					return
				}
				if c, ok := a.Models[model]; !ok || !c.Enabled {
					continue
				}
				ctx, cancel := context.WithTimeout(s.ctx, e.opts.CleanupTimeout)
				err := e.startHarvest(ctx, s, a.AccountID, model, false)
				cancel()
				if err != nil && !errors.Is(err, ErrLeaseBusy) && !errors.Is(err, ErrDisabled) && !errors.Is(err, context.Canceled) {
					e.log(Guard{AccountID: a.AccountID, Model: model}, "harvest_not_started")
				}
			}
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func needsHarvest(slot Slot, plan string, now time.Time) bool {
	return !usable(slot.Active, plan, now) || !usable(slot.Ready, plan, now) || !slot.Ready.ExpiresAt.After(now.Add(RefreshBefore))
}

func (e *Engine) startHarvest(ctx context.Context, s snapshot, id int64, model string, force bool) error {
	if s.config.HarvestProxyURL == "" {
		return ErrUnavailable
	}
	key := slotKey(id, model)
	e.mu.Lock()
	if !e.current.active || e.current.generation != s.generation || e.closed {
		e.mu.Unlock()
		return ErrDisabled
	}
	if _, ok := e.jobs[key]; ok {
		e.mu.Unlock()
		return nil
	}
	jobCtx, cancel := context.WithCancel(s.ctx)
	j := &job{cancel: cancel, generation: s.generation}
	e.jobs[key] = j
	e.wg.Add(1)
	e.mu.Unlock()
	started := false
	defer func() {
		if !started {
			cancel()
			e.finishJob(key, j)
			e.wg.Done()
		}
	}()
	g, _, err := e.guard(ctx, s, id, model)
	if err != nil {
		return err
	}
	cfg, _ := s.config.model(id, model)
	proceed := false
	slot, err := e.mutate(ctx, s, g, nil, func(slot *Slot) (bool, error) {
		proceed = false
		if slot.Harvesting && e.opts.Now().Before(slot.HarvestUntil) {
			return false, ErrLeaseBusy
		}
		changed := normalizeSlot(slot, cfg.TicketPlan, e.opts.Now())
		if slot.Paused {
			return changed, nil
		}
		if e.opts.Now().Before(slot.CooldownUntil) {
			return changed, nil
		}
		if !force && !slot.HarvestRequested && !needsHarvest(*slot, cfg.TicketPlan, e.opts.Now()) {
			return changed, nil
		}
		// A crashed worker resumes its persisted attempt budget after lease expiry.
		if !slot.Harvesting {
			slot.Attempts = 0
		}
		slot.Harvesting = true
		slot.HarvestRequested = false
		slot.JobEpoch++
		slot.HarvestUntil = e.opts.Now().Add(e.opts.LeaseTTL)
		slot.LastError = ""
		proceed = true
		return true, nil
	})
	if err != nil || !proceed {
		return err
	}
	started = true
	go func() {
		defer e.wg.Done()
		defer e.finishJob(key, j)
		defer cancel()
		e.harvest(jobCtx, s, g, slot.JobEpoch, slot.Attempts, cfg.TicketPlan)
	}()
	return nil
}

func (e *Engine) finishJob(key string, j *job) {
	e.mu.Lock()
	if e.jobs[key] == j {
		delete(e.jobs, key)
	}
	e.mu.Unlock()
}

func (e *Engine) harvest(parent context.Context, s snapshot, g Guard, epoch uint64, attempts int, plan string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		interval := e.opts.LeaseTTL / 3
		if e.opts.PollInterval < interval {
			interval = e.opts.PollInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(ctx, e.opts.CleanupTimeout)
				current, _, err := e.guard(checkCtx, s, g.AccountID, g.Model)
				if err == nil && current != g {
					err = ErrStale
				}
				if err == nil {
					var slot Slot
					slot, _, err = e.read(checkCtx, g)
					if err == nil && (!slot.Harvesting || slot.JobEpoch != epoch) {
						err = ErrStale
					}
				}
				if err == nil {
					_, err = e.mutate(checkCtx, s, g, nil, func(slot *Slot) (bool, error) {
						if !slot.Harvesting || slot.JobEpoch != epoch || !e.opts.Now().Before(slot.HarvestUntil) {
							return false, ErrStale
						}
						slot.HarvestUntil = e.opts.Now().Add(e.opts.LeaseTTL)
						return true, nil
					})
				}
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-renewDone }()
	lastError := "harvest_failed"
	success := false
	defer func() {
		// A cancelled/replaced round is deliberately not allowed to publish status.
		if ctx.Err() != nil || !e.live(s) {
			return
		}
		finishCtx, stop := context.WithTimeout(context.Background(), e.opts.CleanupTimeout)
		defer stop()
		_, err := e.mutate(finishCtx, s, g, nil, func(slot *Slot) (bool, error) {
			if slot.JobEpoch != epoch || !slot.Harvesting || !e.opts.Now().Before(slot.HarvestUntil) {
				return false, ErrStale
			}
			slot.Harvesting = false
			if success {
				slot.LastError = ""
				slot.CooldownUntil = time.Time{}
			} else {
				slot.LastError = lastError
				slot.CooldownUntil = e.opts.Now().Add(RetryCooldown)
			}
			return true, nil
		})
		if err != nil {
			e.log(g, "harvest_finalize_failed")
		} else if !success {
			e.log(g, lastError)
		}
	}()
	for attempt := attempts + 1; attempt <= MaxAttempts; attempt++ {
		if ctx.Err() != nil || !e.live(s) {
			return
		}
		current, identity, err := e.guard(ctx, s, g.AccountID, g.Model)
		if err != nil || current != g {
			cancel()
			return
		}
		_, err = e.mutate(ctx, s, g, nil, func(slot *Slot) (bool, error) {
			if slot.JobEpoch != epoch || !slot.Harvesting || !e.opts.Now().Before(slot.HarvestUntil) {
				return false, ErrStale
			}
			slot.Attempts = attempt
			return true, nil
		})
		if err != nil {
			cancel()
			return
		}
		probeCtx, stop := context.WithTimeout(ctx, e.opts.ProbeTimeout)
		result, probeErr := e.prober.Probe(probeCtx, ProbeRequest{Identity: identity, Model: g.Model, ProxyURL: freshProxy(s.config.HarvestProxyURL), DialProxyURL: s.config.DialProxyURL})
		stop()
		if ctx.Err() != nil {
			return
		}
		if isRejection(result.StatusCode) {
			lastError = "upstream_rejected"
			return
		}
		env, envelopeErr := ParseEnvelope(result.State, plan, e.opts.Now())
		if probeErr == nil && result.StatusCode == 200 && result.Completed && result.Model == g.Model && envelopeErr == nil && env.Usable(e.opts.Now()) {
			current, identity, err = e.guard(ctx, s, g.AccountID, g.Model)
			if err != nil || current != g {
				cancel()
				return
			}
			verified := false
			for _, egress := range identity.Egresses {
				if ctx.Err() != nil {
					return
				}
				if ValidateProxy(egress.ProxyURL, false) != nil {
					continue
				}
				verifyCtx, finish := context.WithTimeout(ctx, e.opts.ProbeTimeout)
				verifiedResult, verifyErr := e.prober.Probe(verifyCtx, ProbeRequest{Identity: identity, Model: g.Model, State: result.State, ProxyURL: egress.ProxyURL})
				finish()
				if isRejection(verifiedResult.StatusCode) {
					lastError = "upstream_rejected"
					return
				}
				if verifyErr == nil && verifiedResult.StatusCode == 200 && verifiedResult.Completed && verifiedResult.Model == g.Model && !abnormal(verifiedResult.State, e.opts.Now()) {
					verified = true
					break
				}
			}
			if verified && ctx.Err() == nil {
				_, err = e.mutate(ctx, s, g, nil, func(slot *Slot) (bool, error) {
					if slot.JobEpoch != epoch || !slot.Harvesting || !e.opts.Now().Before(slot.HarvestUntil) {
						return false, ErrStale
					}
					if !env.Usable(e.opts.Now()) {
						return false, ErrUnavailable
					}
					normalizeSlot(slot, plan, e.opts.Now())
					if slot.Active != nil && slot.Active.Fingerprint == env.Fingerprint {
						return false, ErrUnavailable
					}
					slot.NextVersion++
					ticket := &Ticket{State: result.State, Fingerprint: env.Fingerprint, Version: slot.NextVersion, CapturedAt: e.opts.Now(), IssuedAt: env.IssuedAt, ExpiresAt: env.ExpiresAt}
					if slot.Active == nil {
						slot.Active = ticket
						slot.Strikes = 0
					} else if slot.Ready == nil || !slot.Ready.ExpiresAt.After(ticket.ExpiresAt) {
						slot.Ready = ticket
					}
					return true, nil
				})
				if err == nil {
					success = true
					e.log(g, "harvest_persisted")
					return
				}
				if !errors.Is(err, ErrUnavailable) {
					lastError = "harvest_persistence_failed"
					return
				}
			}
		}
		if attempt < MaxAttempts && pause(ctx, e.opts.RetryDelay) != nil {
			return
		}
	}
}

func isRejection(status int) bool { return status == 401 || status == 403 || status == 429 }

var sidPattern = regexp.MustCompile(`(?i)-sid-[^-]+(-t-[0-9]+)`)

func freshProxy(raw string) string {
	u, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || u.User == nil {
		return raw
	}
	username := u.User.Username()
	sid := randomID()[:20]
	if strings.Contains(username, "{sid}") {
		username = strings.ReplaceAll(username, "{sid}", sid)
	} else if strings.HasSuffix(strings.ToLower(u.Hostname()), ".1024proxy.io") || strings.EqualFold(u.Hostname(), "1024proxy.io") {
		username = sidPattern.ReplaceAllString(username, "-sid-"+sid+"${1}")
	}
	if password, ok := u.User.Password(); ok {
		u.User = url.UserPassword(username, password)
	} else {
		u.User = url.User(username)
	}
	return u.String()
}
