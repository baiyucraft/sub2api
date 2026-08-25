package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUsageRequestType(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		input   string
		want    RequestType
		wantErr bool
	}

	cases := []testCase{
		{name: "unknown", input: "unknown", want: RequestTypeUnknown},
		{name: "sync", input: "sync", want: RequestTypeSync},
		{name: "stream", input: "stream", want: RequestTypeStream},
		{name: "ws_v2", input: "ws_v2", want: RequestTypeWSV2},
		{name: "case_insensitive", input: "WS_V2", want: RequestTypeWSV2},
		{name: "trim_spaces", input: "  stream  ", want: RequestTypeStream},
		{name: "invalid", input: "xxx", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseUsageRequestType(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRequestTypeNormalizeAndString(t *testing.T) {
	t.Parallel()

	require.Equal(t, RequestTypeUnknown, RequestType(99).Normalize())
	require.Equal(t, "unknown", RequestType(99).String())
	require.Equal(t, "sync", RequestTypeSync.String())
	require.Equal(t, "stream", RequestTypeStream.String())
	require.Equal(t, "ws_v2", RequestTypeWSV2.String())
}

func TestRequestTypeFromLegacy(t *testing.T) {
	t.Parallel()

	require.Equal(t, RequestTypeWSV2, RequestTypeFromLegacy(false, true))
	require.Equal(t, RequestTypeStream, RequestTypeFromLegacy(true, false))
	require.Equal(t, RequestTypeSync, RequestTypeFromLegacy(false, false))
}

func TestApplyLegacyRequestFields(t *testing.T) {
	t.Parallel()

	stream, ws := ApplyLegacyRequestFields(RequestTypeSync, true, true)
	require.False(t, stream)
	require.False(t, ws)

	stream, ws = ApplyLegacyRequestFields(RequestTypeStream, false, true)
	require.True(t, stream)
	require.False(t, ws)

	stream, ws = ApplyLegacyRequestFields(RequestTypeWSV2, false, false)
	require.True(t, stream)
	require.True(t, ws)

	stream, ws = ApplyLegacyRequestFields(RequestTypeUnknown, true, false)
	require.True(t, stream)
	require.False(t, ws)
}

func TestUsageLogSyncRequestTypeAndLegacyFields(t *testing.T) {
	t.Parallel()

	log := &UsageLog{RequestType: RequestTypeWSV2, Stream: false, OpenAIWSMode: false}
	log.SyncRequestTypeAndLegacyFields()

	require.Equal(t, RequestTypeWSV2, log.RequestType)
	require.True(t, log.Stream)
	require.True(t, log.OpenAIWSMode)
}

func TestUsageLogEffectiveRequestTypeFallback(t *testing.T) {
	t.Parallel()

	log := &UsageLog{RequestType: RequestTypeUnknown, Stream: true, OpenAIWSMode: true}
	require.Equal(t, RequestTypeWSV2, log.EffectiveRequestType())
}

func TestUsageLogEffectiveRequestTypeNilReceiver(t *testing.T) {
	t.Parallel()

	var log *UsageLog
	require.Equal(t, RequestTypeUnknown, log.EffectiveRequestType())
}

func TestUsageLogSyncRequestTypeAndLegacyFieldsNilReceiver(t *testing.T) {
	t.Parallel()

	var log *UsageLog
	log.SyncRequestTypeAndLegacyFields()
}

func TestCalculateOutputTPS(t *testing.T) {
	t.Parallel()

	duration := int64(3000)
	firstToken := int64(1000)
	got := CalculateOutputTPS(400, &duration, &firstToken)
	require.NotNil(t, got)
	require.InDelta(t, 200.0, *got, 1e-12)

	for _, tc := range []struct {
		name         string
		outputTokens int64
		durationMs   *int64
		firstTokenMs *int64
	}{
		{name: "zero output", outputTokens: 0, durationMs: &duration, firstTokenMs: &firstToken},
		{name: "missing duration", outputTokens: 10, durationMs: nil, firstTokenMs: &firstToken},
		{name: "missing first token", outputTokens: 10, durationMs: &duration, firstTokenMs: nil},
		{name: "same timing", outputTokens: 10, durationMs: &firstToken, firstTokenMs: &firstToken},
		{name: "first token after duration", outputTokens: 10, durationMs: &firstToken, firstTokenMs: &duration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Nil(t, CalculateOutputTPS(tc.outputTokens, tc.durationMs, tc.firstTokenMs))
		})
	}
}

func TestUsageLogOutputTPSExcludesMedia(t *testing.T) {
	t.Parallel()
	duration := 3000
	firstToken := 1000

	textLog := &UsageLog{OutputTokens: 400, DurationMs: &duration, FirstTokenMs: &firstToken}
	require.NotNil(t, textLog.OutputTPS())

	imageLog := &UsageLog{OutputTokens: 400, ImageCount: 1, DurationMs: &duration, FirstTokenMs: &firstToken}
	require.Nil(t, imageLog.OutputTPS())
	videoLog := &UsageLog{OutputTokens: 400, VideoCount: 1, DurationMs: &duration, FirstTokenMs: &firstToken}
	require.Nil(t, videoLog.OutputTPS())

	imageBillingMode := string(BillingModeImage)
	imageModeLog := &UsageLog{OutputTokens: 400, BillingMode: &imageBillingMode, DurationMs: &duration, FirstTokenMs: &firstToken}
	require.Nil(t, imageModeLog.OutputTPS())
	videoMediaType := "video/mp4"
	videoMediaLog := &UsageLog{OutputTokens: 400, MediaType: &videoMediaType, DurationMs: &duration, FirstTokenMs: &firstToken}
	require.Nil(t, videoMediaLog.OutputTPS())
}
