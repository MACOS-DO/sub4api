package service

import (
	"net/http"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTLSHTTPClientCachesByProfileIdentity(t *testing.T) {
	dialer := &coderOpenAIWSClientDialer{proxyClients: map[string]*openAIWSProxyClientEntry{}}

	rustlsA, err := dialer.tlsHTTPClient("", tlsfingerprint.CodexRustlsProfile())
	require.NoError(t, err)
	rustlsB, err := dialer.tlsHTTPClient("", tlsfingerprint.CodexRustlsProfile())
	require.NoError(t, err)
	require.Same(t, rustlsA, rustlsB, "same profile must reuse the cached client")

	openssl, err := dialer.tlsHTTPClient("", tlsfingerprint.CodexOpenSSLProfile())
	require.NoError(t, err)
	require.NotSame(t, rustlsA, openssl, "different profiles must not share a client")

	transport, ok := rustlsA.Transport.(*http.Transport)
	require.True(t, ok)
	require.False(t, transport.ForceAttemptHTTP2, "fingerprinted WS must stay on HTTP/1.1")
	require.NotNil(t, transport.DialTLSContext)
	require.NotNil(t, transport.TLSNextProto)
}

func TestOpenAIWSDialWithTLSPrefersFingerprintDialer(t *testing.T) {
	// 非默认拨号器仍可被 dialConn 安全降级（类型断言失败时走 Dial）。
	var plain openAIWSClientDialer = &openAIWSFakeDialer{}
	_, ok := plain.(openAIWSTLSClientDialer)
	require.False(t, ok)

	var coder openAIWSClientDialer = &coderOpenAIWSClientDialer{}
	_, ok = coder.(openAIWSTLSClientDialer)
	require.True(t, ok)
}
