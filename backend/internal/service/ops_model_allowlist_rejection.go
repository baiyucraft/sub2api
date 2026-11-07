package service

import (
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
)

const (
	OpsErrorReasonModelNotAllowed = "model_not_allowed"
	opsModelAllowlistRejectionKey = "ops_model_allowlist_rejection"
)

// OpsModelAllowlistRejection is an admission-time snapshot. It deliberately has
// no request body, credentials, account or upstream fields: later WS turns must
// not inherit attribution from the last successful upstream request.
type OpsModelAllowlistRejection struct {
	Model      string
	Platform   string
	UserID     int64
	APIKeyID   int64
	GroupID    int64
	Stream     bool
	WebSocket  bool
	RejectedAt time.Time
}

func MarkOpsModelAllowlistRejection(c *gin.Context, apiKey *APIKey, model string, stream, websocket bool) {
	if c == nil || apiKey == nil || apiKey.Group == nil {
		return
	}
	if _, exists := c.Get(opsModelAllowlistRejectionKey); exists {
		return
	}
	// Both legacy model columns are VARCHAR(100). Bound before copying and strip
	// control characters so an arbitrary client model cannot create log payloads.
	model = strings.TrimSpace(model)
	characters := 0
	for index := range model {
		if characters == 100 {
			model = model[:index]
			break
		}
		characters++
	}
	model = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, model)
	rejection := OpsModelAllowlistRejection{
		Model: model, Platform: apiKey.Group.Platform, UserID: apiKey.UserID,
		APIKeyID: apiKey.ID, GroupID: apiKey.Group.ID, Stream: stream,
		WebSocket: websocket, RejectedAt: time.Now(),
	}
	if rejection.UserID == 0 && apiKey.User != nil {
		rejection.UserID = apiKey.User.ID
	}
	if rejection.GroupID == 0 && apiKey.GroupID != nil {
		rejection.GroupID = *apiKey.GroupID
	}
	c.Set(opsModelAllowlistRejectionKey, rejection)
}

func GetOpsModelAllowlistRejection(c *gin.Context) (OpsModelAllowlistRejection, bool) {
	if c == nil {
		return OpsModelAllowlistRejection{}, false
	}
	value, ok := c.Get(opsModelAllowlistRejectionKey)
	if !ok {
		return OpsModelAllowlistRejection{}, false
	}
	rejection, ok := value.(OpsModelAllowlistRejection)
	return rejection, ok
}
