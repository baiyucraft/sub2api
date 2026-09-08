package observer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/wire"
)

const (
	outputPath    = service.GatewayRequestObserverOutputPath
	maxBodyBytes  = 256 * 1024
	queueCapacity = 256
)

type Settings = service.GatewayRequestObserverSettings

type GatewayRequestObserver interface {
	Middleware() gin.HandlerFunc
	Apply(context.Context, Settings) error
	Settings(context.Context) (Settings, error)
	Active() bool
	Shutdown(context.Context) error
}

type Service struct {
	current    atomic.Pointer[runtimeState]
	applyMu    sync.Mutex
	outputPath string
	maxBody    int
	queueSize  int
}

type runtimeState struct {
	generation       uint64
	apiKeyIDs        map[int64]struct{}
	apiKeyNames      map[string]struct{}
	userIDs          map[int64]struct{}
	userEmails       map[string]struct{}
	legacyAccountIDs map[int64]struct{}
	queue            chan []byte
	closed           bool
	mu               sync.Mutex
	wg               sync.WaitGroup
	settings         Settings
	outputPath       string
	maxBody          int
}

type observation struct {
	ObservedAt           time.Time `json:"observed_at"`
	RequestID            string    `json:"request_id,omitempty"`
	ClientRequestID      string    `json:"client_request_id,omitempty"`
	UserID               int64     `json:"user_id,omitempty"`
	APIKeyID             int64     `json:"api_key_id,omitempty"`
	APIKeyName           string    `json:"api_key_name,omitempty"`
	MatchedBy            []string  `json:"matched_by"`
	AccountID            int64     `json:"account_id,omitempty"`
	Method               string    `json:"method"`
	Path                 string    `json:"path"`
	Route                string    `json:"route,omitempty"`
	Query                string    `json:"query,omitempty"`
	UserAgent            string    `json:"user_agent,omitempty"`
	RequestContentType   string    `json:"request_content_type,omitempty"`
	RequestBody          string    `json:"request_body,omitempty"`
	RequestBodySHA256    string    `json:"request_body_sha256,omitempty"`
	CapturedBodyBytes    int       `json:"captured_body_bytes,omitempty"`
	RequestBodyTruncated bool      `json:"request_body_truncated,omitempty"`
	StatusCode           int       `json:"status_code"`
	ResponseContentType  string    `json:"response_content_type,omitempty"`
	ResponseBytes        int       `json:"response_bytes,omitempty"`
	DurationMilliseconds int64     `json:"duration_ms"`
}

var ProviderSet = wire.NewSet(NewService)

func NewService() *Service {
	return newService(outputPath, maxBodyBytes, queueCapacity)
}

func newService(path string, bodyLimit, queueLimit int) *Service {
	if strings.TrimSpace(path) == "" {
		path = outputPath
	}
	if bodyLimit <= 0 {
		bodyLimit = maxBodyBytes
	}
	if queueLimit <= 0 {
		queueLimit = queueCapacity
	}
	return &Service{outputPath: path, maxBody: bodyLimit, queueSize: queueLimit}
}

func (s *Service) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := s.current.Load()
		if state == nil || state.queue == nil {
			c.Next()
			return
		}
		apiKey, hasAPIKey := middleware.GetAPIKeyFromContext(c)
		apiKeyMatched := hasAPIKey && matchesAPIKey(state, apiKey)
		userID, userEmail := observedUser(c, apiKey)
		userMatches := matchedUserSources(state, userID, userEmail)
		userMatched := len(userMatches) > 0
		needsBodyCapture := apiKeyMatched || userMatched || len(state.legacyAccountIDs) > 0
		var bodyReader *observedBodyReader
		if needsBodyCapture {
			bodyReader = wrapObservedBody(c.Request, state.maxBody)
		}
		startedAt := time.Now()
		c.Next()
		if s.current.Load() != state {
			return
		}
		accountID := observedAccountID(c)
		_, legacyAccountMatched := state.legacyAccountIDs[accountID]
		if !apiKeyMatched && !userMatched && !legacyAccountMatched {
			return
		}
		capturedBody, capturedBytes, truncated, bodyHash := "", 0, false, ""
		if bodyReader != nil {
			raw, isTruncated, digest := bodyReader.snapshot()
			capturedBody = service.RedactAuditBody(raw, c.Request.Header.Get("Content-Type"))
			capturedBytes, truncated, bodyHash = len(raw), isTruncated, digest
		}
		matchedBy := make([]string, 0, 2)
		if apiKeyMatched {
			matchedBy = append(matchedBy, "api_key")
		}
		matchedBy = append(matchedBy, userMatches...)
		if legacyAccountMatched {
			matchedBy = append(matchedBy, "legacy_account_id")
		}
		entry := observation{
			ObservedAt: startedAt.UTC(), RequestID: observedStringValue(c, ctxkey.RequestID),
			ClientRequestID: observedStringValue(c, ctxkey.ClientRequestID), MatchedBy: matchedBy,
			AccountID: accountID, Method: c.Request.Method, Path: c.Request.URL.Path,
			Route: c.FullPath(), Query: service.RedactAuditQuery(c.Request.URL.RawQuery),
			UserAgent: c.Request.UserAgent(), RequestContentType: c.Request.Header.Get("Content-Type"),
			RequestBody: capturedBody, RequestBodySHA256: bodyHash, CapturedBodyBytes: capturedBytes,
			RequestBodyTruncated: truncated, StatusCode: c.Writer.Status(),
			ResponseContentType: c.Writer.Header().Get("Content-Type"), ResponseBytes: responseBytes(c),
			DurationMilliseconds: time.Since(startedAt).Milliseconds(),
		}
		if hasAPIKey && apiKey != nil {
			entry.APIKeyID, entry.APIKeyName = apiKey.ID, apiKey.Name
		}
		entry.UserID = userID
		state.enqueue(entry)
	}
}

func (s *Service) Apply(_ context.Context, settings Settings) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	normalizeSettings(&settings)
	if err := validateSettings(settings); err != nil {
		return err
	}
	previous := s.current.Load()
	if previous != nil && sameSettings(previous.settings, settings) {
		return nil
	}
	next := s.newRuntimeState(settings, generationOf(previous)+1)
	s.current.Store(next)
	if previous != nil {
		previous.stop()
	}
	return nil
}

func (s *Service) Current() Settings {
	if state := s.current.Load(); state != nil {
		return cloneSettings(state.settings)
	}
	return Settings{APIKeyIDs: []int64{}, APIKeyNames: []string{}, UserIDs: []int64{}, UserEmails: []string{}}
}

func (s *Service) Settings(context.Context) (Settings, error) {
	return s.Current(), nil
}

func (s *Service) Active() bool {
	state := s.current.Load()
	return state != nil && state.queue != nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.applyMu.Lock()
	state := s.current.Swap(nil)
	s.applyMu.Unlock()
	if state == nil {
		return nil
	}
	return state.stopContext(ctx)
}

func (s *Service) newRuntimeState(settings Settings, generation uint64) *runtimeState {
	path, bodyLimit, queueLimit := s.runtimeDefaults()
	state := &runtimeState{
		generation:       generation,
		apiKeyIDs:        make(map[int64]struct{}, len(settings.APIKeyIDs)),
		apiKeyNames:      make(map[string]struct{}, len(settings.APIKeyNames)),
		userIDs:          make(map[int64]struct{}, len(settings.UserIDs)),
		userEmails:       make(map[string]struct{}, len(settings.UserEmails)),
		legacyAccountIDs: make(map[int64]struct{}, len(settings.LegacyAccountIDs)),
		settings:         cloneSettings(settings), outputPath: path, maxBody: bodyLimit,
	}
	for _, id := range settings.APIKeyIDs {
		state.apiKeyIDs[id] = struct{}{}
	}
	for _, name := range settings.APIKeyNames {
		state.apiKeyNames[strings.ToLower(name)] = struct{}{}
	}
	for _, id := range settings.UserIDs {
		state.userIDs[id] = struct{}{}
	}
	for _, email := range settings.UserEmails {
		state.userEmails[strings.ToLower(email)] = struct{}{}
	}
	for _, id := range settings.LegacyAccountIDs {
		state.legacyAccountIDs[id] = struct{}{}
	}
	if !settings.Enabled || len(state.apiKeyIDs) == 0 && len(state.apiKeyNames) == 0 && len(state.userIDs) == 0 && len(state.userEmails) == 0 && len(state.legacyAccountIDs) == 0 {
		return state
	}
	state.queue = make(chan []byte, queueLimit)
	state.wg.Add(1)
	go state.writeLoop()
	return state
}

func (s *Service) runtimeDefaults() (string, int, int) {
	if s == nil {
		return outputPath, maxBodyBytes, queueCapacity
	}
	path, bodyLimit, queueLimit := s.outputPath, s.maxBody, s.queueSize
	if strings.TrimSpace(path) == "" {
		path = outputPath
	}
	if bodyLimit <= 0 {
		bodyLimit = maxBodyBytes
	}
	if queueLimit <= 0 {
		queueLimit = queueCapacity
	}
	return path, bodyLimit, queueLimit
}

func (s *runtimeState) enqueue(entry observation) {
	record, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("gateway observer marshal failed", "error", err)
		return
	}
	record = append(record, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.queue == nil {
		return
	}
	select {
	case s.queue <- record:
	default:
		slog.Warn("gateway observer queue full; dropping observation")
	}
}

func (s *runtimeState) stop() { _ = s.stopContext(context.Background()) }

func (s *runtimeState) stopContext(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.queue != nil {
			close(s.queue)
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *runtimeState) writeLoop() {
	defer s.wg.Done()
	directory := filepath.Dir(s.outputPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		slog.Warn("gateway observer directory creation failed", "error", err)
		for range s.queue {
		}
		return
	}
	file, err := os.OpenFile(s.outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		slog.Warn("gateway observer file open failed", "error", err)
		for range s.queue {
		}
		return
	}
	defer file.Close()
	for record := range s.queue {
		if _, err := file.Write(record); err != nil {
			slog.Warn("gateway observer write failed", "error", err)
		}
	}
}

func normalizeSettings(settings *Settings) {
	settings.APIKeyIDs = normalizeIDs(settings.APIKeyIDs)
	settings.UserIDs = normalizeIDs(settings.UserIDs)
	settings.LegacyAccountIDs = normalizeIDs(settings.LegacyAccountIDs)
	settings.APIKeyNames = normalizeNames(settings.APIKeyNames)
	settings.UserEmails = normalizeEmails(settings.UserEmails)
}

func validateSettings(settings Settings) error {
	if settings.Enabled && len(settings.APIKeyIDs) == 0 && len(settings.APIKeyNames) == 0 && len(settings.UserIDs) == 0 && len(settings.UserEmails) == 0 && len(settings.LegacyAccountIDs) == 0 {
		return os.ErrInvalid
	}
	return nil
}

func sameSettings(a, b Settings) bool {
	return a.Enabled == b.Enabled && equalInt64s(a.APIKeyIDs, b.APIKeyIDs) &&
		equalInt64s(a.UserIDs, b.UserIDs) && equalInt64s(a.LegacyAccountIDs, b.LegacyAccountIDs) && equalStrings(a.APIKeyNames, b.APIKeyNames) && equalStrings(a.UserEmails, b.UserEmails)
}

func cloneSettings(in Settings) Settings {
	return Settings{
		Enabled: in.Enabled, APIKeyIDs: append([]int64(nil), in.APIKeyIDs...),
		APIKeyNames: append([]string(nil), in.APIKeyNames...), UserIDs: append([]int64(nil), in.UserIDs...), UserEmails: append([]string(nil), in.UserEmails...), LegacyAccountIDs: append([]int64(nil), in.LegacyAccountIDs...),
	}
}

func generationOf(state *runtimeState) uint64 {
	if state == nil {
		return 0
	}
	return state.generation
}

func normalizeIDs(values []int64) []int64 {
	seen := map[int64]struct{}{}
	result := []int64{}
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeNames(values []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeEmails(values []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func matchesAPIKey(state *runtimeState, key *service.APIKey) bool {
	if key == nil {
		return false
	}
	if _, ok := state.apiKeyIDs[key.ID]; ok {
		return true
	}
	_, ok := state.apiKeyNames[strings.ToLower(strings.TrimSpace(key.Name))]
	return ok
}

func matchedUserSources(state *runtimeState, userID int64, userEmail string) []string {
	matchedBy := make([]string, 0, 2)
	if userID > 0 {
		if _, ok := state.userIDs[userID]; ok {
			matchedBy = append(matchedBy, "user_id")
		}
	}
	if _, ok := state.userEmails[strings.ToLower(strings.TrimSpace(userEmail))]; ok {
		matchedBy = append(matchedBy, "user_email")
	}
	return matchedBy
}

func observedUser(c *gin.Context, apiKey *service.APIKey) (int64, string) {
	var userID int64
	var email string
	if apiKey != nil {
		userID, email = apiKey.UserID, ""
		if apiKey.User != nil {
			email = apiKey.User.Email
			if apiKey.User.ID > 0 {
				userID = apiKey.User.ID
			}
		}
	}
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		userID = subject.UserID
	}
	return userID, email
}

func observedAccountID(c *gin.Context) int64 {
	if c == nil || c.Request == nil {
		return 0
	}
	if id, ok := c.Request.Context().Value(ctxkey.AccountID).(int64); ok {
		return id
	}
	if value, ok := c.Get("ops_account_id"); ok {
		if id, ok := value.(int64); ok {
			return id
		}
	}
	return 0
}

func observedStringValue(c *gin.Context, key ctxkey.Key) string {
	if c == nil || c.Request == nil {
		return ""
	}
	value, _ := c.Request.Context().Value(key).(string)
	return value
}

func responseBytes(c *gin.Context) int {
	if c == nil || c.Writer == nil || c.Writer.Size() < 0 {
		return 0
	}
	return c.Writer.Size()
}

func wrapObservedBody(request *http.Request, limit int) *observedBodyReader {
	if request == nil || request.Body == nil {
		return nil
	}
	reader := &observedBodyReader{ReadCloser: request.Body, maxBytes: limit}
	request.Body = reader
	return reader
}

type observedBodyReader struct {
	io.ReadCloser
	maxBytes  int
	captured  []byte
	truncated bool
	digest    hash.Hash
}

func (r *observedBodyReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n <= 0 {
		return n, err
	}
	if r.digest == nil {
		r.digest = sha256.New()
	}
	_, _ = r.digest.Write(p[:n])
	remaining := r.maxBytes - len(r.captured)
	if remaining > 0 {
		captureBytes := n
		if captureBytes > remaining {
			captureBytes = remaining
		}
		r.captured = append(r.captured, p[:captureBytes]...)
	}
	if n > remaining {
		r.truncated = true
	}
	return n, err
}

func (r *observedBodyReader) snapshot() ([]byte, bool, string) {
	if r.digest == nil {
		return append([]byte(nil), r.captured...), r.truncated, ""
	}
	return append([]byte(nil), r.captured...), r.truncated, hex.EncodeToString(r.digest.Sum(nil))
}
