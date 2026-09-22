package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type upgradeReviewRepository struct {
	PluginRepository
	installations []*PluginInstallation
}

func (r *upgradeReviewRepository) List(context.Context) ([]*PluginInstallation, error) {
	return r.installations, nil
}

func (r *upgradeReviewRepository) GetByID(_ context.Context, id int64) (*PluginInstallation, error) {
	for _, installation := range r.installations {
		if installation.ID == id {
			return installation, nil
		}
	}
	return nil, fmt.Errorf("installation %d not found", id)
}

func TestPluginUpgradeReviewSwitchedBindingCannotBypassAdmission(t *testing.T) {
	old := &PluginInstallation{
		ID: 1, State: PluginStateDisabled, ConfigRevision: 1,
		Manifest:     PluginManifest{Requires: PluginRequirements{HostServiceAPI: 2, HostFeatures: []string{"scoped-routing.v1"}}},
		ManagedScope: []PluginManagedTarget{{AccountID: 41, Models: []string{"model-a"}}},
	}
	current := &PluginInstallation{
		ID: 2, State: PluginStateEnabled, ConfigRevision: 1,
		Manifest:     PluginManifest{Requires: PluginRequirements{HostServiceAPI: 2, HostFeatures: []string{"scoped-routing.v1"}}},
		ManagedScope: []PluginManagedTarget{{AccountID: 42, Models: []string{"model-b"}}},
		Bindings:     []PluginBinding{{Capability: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth, Enabled: true, RolloutPercent: 100}},
	}
	manager := &PluginManager{repo: &upgradeReviewRepository{installations: []*PluginInstallation{old, current}}}
	// Another instance switched the binding; this instance has not reconciled yet.
	manager.route.Store(scopedPluginRoute(old, nil, ""))
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	if manager.AdmitOpenAIAccount(context.Background(), account, "model-b") {
		t.Error("newly managed account admitted without the active plugin")
	}
	ctx := context.WithValue(context.Background(), pluginRequestMetadataKey{}, pluginRequestMetadata{Model: "model-b"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, handled, err := manager.RoundTripOpenAIOAuth(ctx, request, "", account)
	if !handled || err == nil {
		t.Errorf("managed request escaped to built-in transport: handled=%t err=%v", handled, err)
	}
}

func TestPluginUpgradeReviewRollbackMustRearmDrainSignal(t *testing.T) {
	runtime := &pluginRuntime{}
	if !runtime.beginRequest() {
		t.Fatal("initial admission failed")
	}
	// Upgrade blocks admission and the final existing request finishes.
	runtime.draining.Store(true)
	runtime.finishRequest()
	// Upgrade.restore currently resumes the same runtime with this operation.
	runtime.draining.Store(false)
	if !runtime.beginRequest() {
		t.Fatal("restored runtime rejected admission")
	}
	drained := make(chan struct{})
	go func() {
		runtime.drain(time.Second)
		close(drained)
	}()
	select {
	case <-drained:
		runtime.finishRequest()
		t.Fatal("restored runtime drained before its new in-flight request finished")
	case <-time.After(25 * time.Millisecond):
	}
	runtime.finishRequest()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not drain after request completion")
	}
}

func TestPluginUpgradeRollbackOnlyResumesExactPackage(t *testing.T) {
	expected := &PluginInstallation{ID: 7, PluginKey: "test.rollback", Version: "1.2.3", BinarySHA256: "fixed"}
	for _, variant := range []string{"exact", "sha", "version", "key", "id", "exited", "missing"} {
		t.Run(variant, func(t *testing.T) {
			local := *expected
			runtime := &pluginRuntime{installation: &local}
			switch variant {
			case "sha":
				local.BinarySHA256 = "stale"
			case "version":
				local.Version = "1.2.2"
			case "key":
				local.PluginKey = "test.other"
			case "id":
				local.ID++
			case "exited":
				runtime.exited.Store(true)
			case "missing":
				runtime = nil
			}
			if got := pluginRuntimeMatchesInstallation(runtime, expected); got != (variant == "exact") {
				t.Fatalf("resumable=%t for %s", got, variant)
			}
		})
	}
}

func TestLegacyPluginUpgradeOnlyAllowsDisabledUnboundInstallation(t *testing.T) {
	legacy := &PluginInstallation{
		Manifest: PluginManifest{SchemaVersion: 1},
		State:    PluginStateDisabled,
	}
	if !legacyPluginUpgradeAllowed(legacy) {
		t.Fatal("disabled legacy installation should be upgradeable through maintenance")
	}

	for _, state := range []string{PluginStateEnabled, PluginStateError, PluginStateStarting, PluginStateUpgrading} {
		legacy.State = state
		if legacyPluginUpgradeAllowed(legacy) {
			t.Fatalf("legacy installation in state %q must require explicit disable and install", state)
		}
	}

	legacy.State = PluginStateDisabled
	legacy.Bindings = []PluginBinding{{
		Capability:     PluginCapabilityOpenAIOAuthOutbound,
		Platform:       PlatformOpenAI,
		AccountType:    AccountTypeOAuth,
		Enabled:        true,
		RolloutPercent: 100,
	}}
	if legacyPluginUpgradeAllowed(legacy) {
		t.Fatal("legacy installation with an enabled binding must not bypass explicit disable")
	}
}

func TestScopedPluginUpgradeRemainsAllowedByMaintenancePolicy(t *testing.T) {
	for _, state := range []string{PluginStateDisabled, PluginStateEnabled, PluginStateError} {
		installation := &PluginInstallation{
			Manifest: PluginManifest{Requires: PluginRequirements{HostFeatures: []string{"scoped-routing.v1"}}},
			State:    state,
		}
		if !legacyPluginUpgradeAllowed(installation) {
			t.Fatalf("scoped plugin in state %q should continue to use the maintenance path", state)
		}
	}
}
