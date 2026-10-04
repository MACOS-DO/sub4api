package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
)

type codexLiveIdentityKey struct{}
type codexLiveHeadersKey struct{}

func (s *OpenAIGatewayService) createCodexGatewayLiveCall(ctx context.Context, account *Account, input *LiveCallRequest, attestation string) (*LiveCallCreated, error) {
	if s.codexGateway == nil || !s.codexGateway.Ready() || account.Gateway == nil {
		return nil, codexgateway.Unavailable()
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	if original, ok := ctx.Value(codexLiveHeadersKey{}).(http.Header); ok {
		headers = original.Clone()
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/sdp")
	headers.Set(liveAttestationHeader, attestation)
	applyLiveUpstreamIdentityHeaders(headers)
	apiKeyID, _ := ctx.Value(codexLiveIdentityKey{}).(int64)
	source, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	identity, err := s.resolveGatewayIdentity(ctx, source, apiKeyID, headers, body)
	if err != nil {
		return nil, err
	}
	response, err := s.codexGateway.client.Forward(ctx, account.Gateway.BindingID, http.MethodPost, "/v1/live", bytes.NewReader(body), headers, identity, account.OpenAIRequestTimezone(), nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if err = codexgateway.DecodeResponse(response); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, liveUpstreamBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > liveUpstreamBodyLimit {
		return nil, errors.New("live upstream response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &UpstreamFailoverError{StatusCode: response.StatusCode, ResponseBody: raw, ResponseHeaders: response.Header.Clone()}
	}
	callID, err := liveCallIDFromLocation(response.Header.Get("Location"))
	if err != nil {
		return nil, err
	}
	return &LiveCallCreated{SDP: raw, CallID: callID, Location: response.Header.Get("Location"), GatewayIdentity: &identity, GatewayTimezone: account.OpenAIRequestTimezone()}, nil
}

func (s *OpenAIGatewayService) dialCodexGatewayLive(ctx context.Context, account *Account, record *LiveCallRecord) (liveFrameConn, error) {
	if s.codexGateway == nil || !s.codexGateway.Ready() || account.Gateway == nil || record.GatewayIdentity == nil {
		return nil, codexgateway.Unavailable()
	}
	attestation, err := s.decryptLiveAttestation(record)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set(liveAttestationHeader, attestation)
	applyLiveUpstreamIdentityHeaders(headers)
	conn, _, err := s.codexGateway.client.Dial(ctx, account.Gateway.BindingID, "/v1/live/"+url.PathEscape(record.CallID), headers, *record.GatewayIdentity, record.GatewayTimezone, nil)
	if err != nil {
		return nil, err
	}
	return &codexGatewayFrameConn{conn: conn, sideband: true}, nil
}
