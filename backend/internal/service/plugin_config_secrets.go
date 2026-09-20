package service

import (
	"context"
	"encoding/json"
	"errors"
)

// Secret field declarations are signed package metadata, not iframe input.
// The runtime receives the full encrypted configuration; browser reads never do.
func pluginPublicConfig(manifest PluginManifest, raw json.RawMessage) (json.RawMessage, error) {
	if len(manifest.ConfigSecrets) == 0 {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("invalid plugin configuration")
	}
	configured := map[string]bool{}
	for _, key := range manifest.ConfigSecrets {
		var value string
		_ = json.Unmarshal(fields[key], &value)
		configured[key] = value != ""
		fields[key] = json.RawMessage(`""`)
	}
	flags, _ := json.Marshal(configured)
	fields["_host_secrets"] = flags
	return json.Marshal(fields)
}

func pluginMergeConfigSecrets(manifest PluginManifest, previous, incoming json.RawMessage, trustedEdit bool) (json.RawMessage, error) {
	var stored, fields map[string]json.RawMessage
	if json.Unmarshal(previous, &stored) != nil || json.Unmarshal(incoming, &fields) != nil || fields == nil {
		return nil, errors.New("invalid plugin configuration")
	}
	delete(fields, "_host_secrets")
	if !trustedEdit {
		for _, key := range manifest.ConfigSecrets {
			if value, ok := stored[key]; ok {
				fields[key] = value
			} else {
				delete(fields, key)
			}
		}
	}
	return json.Marshal(fields)
}

// SaveConfigSecrets is exposed only by the host's trusted administrative form.
// Omitted fields are kept, empty strings clear, and non-empty strings replace.
func (m *PluginManager) SaveConfigSecrets(ctx context.Context, id int64, values map[string]string) (json.RawMessage, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	installation, err := m.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, key := range installation.Manifest.ConfigSecrets {
		allowed[key] = true
	}
	if len(values) == 0 || len(values) > len(allowed) {
		return nil, errors.New("invalid secret field update")
	}
	raw, err := m.decryptConfig(installation)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("invalid plugin configuration")
	}
	for key, value := range values {
		if !allowed[key] || len(value) > 8192 {
			return nil, errors.New("invalid secret field update")
		}
		fields[key], _ = json.Marshal(value)
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return m.saveConfigLocked(ctx, id, raw, true, &installation.ConfigRevision)
}
