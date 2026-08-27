package service

import "time"

type AccountGroup struct {
	AccountID          int64
	GroupID            int64
	Priority           int
	SchedulerPreferred bool
	CreatedAt          time.Time

	Account *Account
	Group   *Group
}
