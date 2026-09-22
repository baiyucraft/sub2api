package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
)

const Version = "0.1.0"

var Models = []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra"}

func defaultConfig() Config {
	return Config{Version: 1, Accounts: []AccountConfig{}}
}

type Config struct {
	Version         int             `json:"version"`
	Enabled         bool            `json:"enabled"`
	HarvestProxyURL string          `json:"harvest_proxy_url"`
	DialProxyURL    string          `json:"dial_proxy_url"`
	Accounts        []AccountConfig `json:"accounts"`
}

type AccountConfig struct {
	AccountID int64                  `json:"account_id"`
	Models    map[string]ModelConfig `json:"models"`
}

type ModelConfig struct {
	Enabled    bool   `json:"enabled"`
	TicketPlan string `json:"ticket_plan"`
}

// Validate is deliberately pure: no host, credentials, transport or goroutine.
// Unknown fields include user-supplied revision fields and are rejected.
func Validate(raw []byte) (Config, error) {
	// Manifest v1 installations may have been created before the plugin had a
	// configuration document. Treat that empty document as a disabled v1
	// configuration so a v1 -> v2 package upgrade can migrate it safely.
	if bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return defaultConfig(), nil
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil {
		return Config{}, errors.New("invalid configuration JSON or unknown field")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Config{}, errors.New("configuration must contain one JSON object")
	}
	if cfg.Version != 1 {
		return Config{}, errors.New("configuration version must be 1")
	}
	if err := ValidateProxy(cfg.HarvestProxyURL, false); err != nil {
		return Config{}, err
	}
	if err := ValidateProxy(cfg.DialProxyURL, true); err != nil {
		return Config{}, err
	}
	seen := map[int64]bool{}
	for _, account := range cfg.Accounts {
		if account.AccountID <= 0 || seen[account.AccountID] {
			return Config{}, errors.New("account_id must be positive and unique")
		}
		seen[account.AccountID] = true
		for model, setting := range account.Models {
			if !supportedModel(model) {
				return Config{}, errors.New("unsupported outbound model")
			}
			if setting.TicketPlan != "pro" && setting.TicketPlan != "team" {
				return Config{}, errors.New("ticket_plan must be pro or team")
			}
		}
	}
	return cfg, nil
}

// Normalize returns the canonical persisted representation used by the host
// when validating an upgrade or saving a configuration draft.
func Normalize(raw []byte) (Config, []byte, error) {
	cfg, err := Validate(raw)
	if err != nil {
		return Config{}, nil, err
	}
	canonical, err := json.Marshal(cfg)
	if err != nil {
		return Config{}, nil, err
	}
	return cfg, canonical, nil
}

func supportedModel(model string) bool {
	switch model {
	case "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra":
		return true
	}
	return false
}

func (c Config) model(id int64, model string) (ModelConfig, bool) {
	for _, a := range c.Accounts {
		if a.AccountID == id {
			v, ok := a.Models[model]
			return v, ok
		}
	}
	return ModelConfig{}, false
}

func ValidateProxy(raw string, dialOnly bool) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid proxy URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" && (dialOnly || (u.Scheme != "socks5" && u.Scheme != "socks5h")) {
		return errors.New("unsupported proxy scheme")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("invalid proxy port")
		}
	}
	return nil
}
