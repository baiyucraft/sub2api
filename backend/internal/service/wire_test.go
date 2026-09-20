package service

import (
	"errors"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/collection"
)

func TestProvidePluginManagerInjectsStateStore(t *testing.T) {
	store := &struct{ PluginStateStore }{}
	directory := &struct{ PluginAccountDirectory }{}
	manager := ProvidePluginManager(nil, nil, nil, PluginHostInfo{}, nil, directory, store)
	if manager.stateStore != store {
		t.Fatal("plugin manager did not receive the durable state store")
	}
	if manager.accountDirectory != directory {
		t.Fatal("plugin manager did not receive the account directory")
	}
}

func TestProvideTimingWheelService_ReturnsError(t *testing.T) {
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, _ collection.Execute) (*collection.TimingWheel, error) {
		return nil, errors.New("boom")
	}

	svc, err := ProvideTimingWheelService()
	if err == nil {
		t.Fatalf("期望返回 error，但得到 nil")
	}
	if svc != nil {
		t.Fatalf("期望返回 nil svc，但得到非空")
	}
}

func TestProvideTimingWheelService_Success(t *testing.T) {
	svc, err := ProvideTimingWheelService()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	if svc == nil {
		t.Fatalf("期望 svc 非空，但得到 nil")
	}
	svc.Stop()
}
