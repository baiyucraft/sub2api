package upstreamcacheguard

import (
	"errors"
	"time"
)

func readAdmission(tx Tx, session *SessionState, r Request) (Admission, bool, error) {
	if split, ok := tx.(AdmissionTx); ok {
		return split.ReadAdmission(SessionKey(r), admissionKey(r))
	}
	a, exists := session.Admissions[admissionKey(r)]
	return a, exists, nil
}

func saveAdmission(tx Tx, session *SessionState, r Request, a Admission) error {
	if split, ok := tx.(AdmissionTx); ok {
		return split.WriteAdmission(SessionKey(r), admissionKey(r), a)
	}
	if session.Admissions == nil {
		session.Admissions = map[string]Admission{}
	}
	session.Admissions[admissionKey(r)] = a
	return nil
}

func checkAdmissionBound(tx Tx, session *SessionState, r Request) error {
	var count int
	if split, ok := tx.(AdmissionTx); ok {
		var err error
		count, err = split.RetainedAdmissionCount(SessionKey(r), r.StartedAt.Add(-FactLifetime))
		if err != nil {
			return err
		}
	} else {
		for key, old := range session.Admissions {
			if old.Completed && r.StartedAt.Sub(old.StartedAt) > FactLifetime {
				delete(session.Admissions, key)
			}
		}
		count = len(session.Admissions)
	}
	// Never discard an unfinished request to make room: its frozen classification
	// and its place in the low-streak sequence must survive other completions.
	if count >= 100000 {
		return errors.New("cache guard session admission bound exceeded")
	}
	return nil
}

func pendingRouteAdmissions(tx Tx, session *SessionState, r Request) ([]Admission, error) {
	if split, ok := tx.(AdmissionTx); ok {
		return split.PendingAdmissions(SessionKey(r), r.AccountID, WarmRouteKey(r))
	}
	var out []Admission
	for _, a := range session.Admissions {
		if !a.Completed && a.AccountID == r.AccountID && (a.RouteKey == WarmRouteKey(r) || a.RouteKey == "") {
			out = append(out, a)
		}
	}
	return out, nil
}

func stalePending(a Admission, bornAt, now time.Time) bool {
	return !a.Completed && (a.StartedAt.Before(bornAt) || now.Sub(a.StartedAt) > 5*time.Minute)
}
