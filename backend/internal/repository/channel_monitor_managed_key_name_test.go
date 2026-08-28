//go:build unit

package repository

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestManagedMonitorKeyNameUsesMonitorNameWithoutID(t *testing.T) {
	monitor := &service.ChannelMonitor{ID: 4, Name: " gpt-低价 "}

	require.Equal(t, "监控-gpt-低价", managedMonitorKeyName(monitor))
}

func TestManagedMonitorKeyNameFitsAPIKeyNameLimit(t *testing.T) {
	monitor := &service.ChannelMonitor{ID: 4, Name: strings.Repeat("渠", 100)}

	name := managedMonitorKeyName(monitor)
	require.Equal(t, 103, utf8.RuneCountInString(name))
	require.Equal(t, "监控-"+strings.Repeat("渠", 100), name)
}

func TestApplyManagedMonitorGroupNameUsesAuthoritativeGroupName(t *testing.T) {
	monitor := &service.ChannelMonitor{
		Name:      "管理员提交的名称",
		GroupName: "旧分组",
	}

	applyManagedMonitorGroupName(monitor, "真实分组")

	require.Equal(t, "真实分组", monitor.Name)
	require.Equal(t, "真实分组", monitor.GroupName)
}

func TestApplyManagedMonitorGroupNameHandlesNilMonitor(t *testing.T) {
	require.NotPanics(t, func() {
		applyManagedMonitorGroupName(nil, "真实分组")
	})
}
