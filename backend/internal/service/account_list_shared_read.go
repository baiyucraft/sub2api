package service

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

type accountListReadAuthorizationKey struct{}
type accountListReadAuthorization struct {
	UserID int64
	Role   string
}

// WithAccountListReadAuthorization scopes concurrent raw reads to one authenticated
// administrator. Anonymous/test callers do not share reads.
func WithAccountListReadAuthorization(ctx context.Context, userID int64, role string) context.Context {
	return context.WithValue(ctx, accountListReadAuthorizationKey{}, accountListReadAuthorization{userID, role})
}

type accountListReadFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	value   any
	err     error
}

var accountListRawFlights = struct {
	sync.Mutex
	active map[string]*accountListReadFlight
}{active: make(map[string]*accountListReadFlight)}

func accountListSharedRead[T any](ctx context.Context, owner, operation string, query any, read func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	authorization, ok := ctx.Value(accountListReadAuthorizationKey{}).(accountListReadAuthorization)
	if !ok || authorization.UserID <= 0 {
		return read(ctx)
	}
	preferred, _ := AccountListPreferredFromContext(ctx)
	key, err := json.Marshal(struct {
		Owner, Operation string
		Authorization    accountListReadAuthorization
		Query            any
		Preferred        bool
		Upstream         AccountListUpstreamIDs
	}{owner, operation, authorization, query, preferred, AccountListUpstreamIDsFromContext(ctx)})
	if err != nil {
		return zero, err
	}
	flightKey := string(key)
	accountListRawFlights.Lock()
	flight := accountListRawFlights.active[flightKey]
	if flight == nil {
		// No single caller owns cancellation; the final waiter releases the read.
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		flight = &accountListReadFlight{done: make(chan struct{}), cancel: cancel}
		accountListRawFlights.active[flightKey] = flight
		go func(current *accountListReadFlight) {
			value, err := read(shared)
			cancel()
			accountListRawFlights.Lock()
			current.value, current.err = value, err
			if accountListRawFlights.active[flightKey] == current {
				delete(accountListRawFlights.active, flightKey)
			}
			close(current.done)
			accountListRawFlights.Unlock()
		}(flight)
	}
	flight.waiters++
	accountListRawFlights.Unlock()
	defer func() {
		accountListRawFlights.Lock()
		flight.waiters--
		if flight.waiters == 0 {
			if accountListRawFlights.active[flightKey] == flight {
				delete(accountListRawFlights.active, flightKey)
			}
			flight.cancel()
		}
		accountListRawFlights.Unlock()
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-flight.done:
		if flight.err != nil {
			return zero, flight.err
		}
		return flight.value.(T), nil
	}
}
