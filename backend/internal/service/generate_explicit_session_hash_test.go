package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayGenerateExplicitSessionHashSkipsContentFallback(t *testing.T) {
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-test","messages":[{"role":"user","content":"same content"}]}`)), "anthropic")
	require.NoError(t, err)
	require.NotEmpty(t, (&GatewayService{}).GenerateSessionHash(parsed))
	require.Empty(t, (&GatewayService{}).GenerateExplicitSessionHash(parsed))
}

func TestGatewayGenerateExplicitSessionHashUsesMetadataSessionID(t *testing.T) {
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	parsed := &ParsedRequest{MetadataUserID: `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","account_uuid":"","session_id":"123e4567-e89b-12d3-a456-426614174000"}`}
	require.Equal(t, sessionID, (&GatewayService{}).GenerateExplicitSessionHash(parsed))
}
