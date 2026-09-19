package service

import (
	"context"
	"io"
	"net/http"
	"testing"

	pluginv1 "github.com/MACOS-DO/sub4api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type diagnosticsPluginClient struct {
	pluginv1.TransportPluginClient
}

func (diagnosticsPluginClient) Forward(ctx context.Context, _ ...grpc.CallOption) (pluginv1.TransportPlugin_ForwardClient, error) {
	return &diagnosticsPluginStream{ctx: ctx, sent: make(chan struct{})}, nil
}

type diagnosticsPluginStream struct {
	grpc.ClientStream
	ctx     context.Context
	started bool
	sent    chan struct{}
}

func (s *diagnosticsPluginStream) Context() context.Context            { return s.ctx }
func (s *diagnosticsPluginStream) Send(*pluginv1.ForwardRequest) error { return nil }
func (s *diagnosticsPluginStream) CloseSend() error {
	close(s.sent)
	return nil
}
func (s *diagnosticsPluginStream) Recv() (*pluginv1.ForwardResponse, error) {
	if s.started {
		<-s.sent
		return nil, io.EOF
	}
	s.started = true
	return &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{
		StatusCode: http.StatusTooManyRequests,
		Headers:    headersToPlugin(http.Header{"Set-Cookie": {"a=1", "b=2"}, "X-Request-Id": {"plugin-response"}}),
	}}}, nil
}

func TestAccountTestObserverRecordsPluginResponseOnce(t *testing.T) {
	runtime := &pluginRuntime{client: &hcplugin.Client{}, api: diagnosticsPluginClient{}}
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, runtime: runtime})
	// A plugin-owned response must not touch the native transport.
	upstream := &httpUpstreamRecorder{}
	svc := &AccountTestService{pluginManager: manager, httpUpstream: upstream}
	var events []AccountTestUpstreamResponse
	ctx := withAccountTestObserver(context.Background(), func(event AccountTestUpstreamResponse) {
		events = append(events, event)
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/responses?key=private", nil)
	require.NoError(t, err)
	resp, err := svc.doOpenAIAccountTestUpstream(req, "", &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, true)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, 1, events[0].Sequence)
	require.Equal(t, http.StatusTooManyRequests, events[0].StatusCode)
	require.Equal(t, "/responses", events[0].Stage)
	require.Equal(t, []string{"a=1", "b=2"}, events[0].Headers.Values("Set-Cookie"))
	require.Empty(t, upstream.requests)
	require.NoError(t, resp.Body.Close())
	require.Zero(t, runtime.inFlight.Load())
	observeAccountTestResponse(ctx, http.MethodGet, "websocket", 101, http.Header{"Upgrade": {"websocket"}})
	require.Len(t, events, 2)
	require.Equal(t, 2, events[1].Sequence, "all transport paths share the test sequence")
}
