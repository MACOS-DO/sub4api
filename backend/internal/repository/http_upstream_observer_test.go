package repository

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/pkg/tlsfingerprint"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/require"
)

func diagnosticsGzip(t *testing.T, body string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := io.WriteString(writer, body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return compressed.Bytes()
}

func diagnosticsContext(events *[]service.AccountTestUpstreamResponse) context.Context {
	return service.WithHTTPUpstreamResponseObserver(context.Background(), func(req *http.Request, resp *http.Response) {
		*events = append(*events, service.AccountTestUpstreamResponse{
			Method: req.Method, Stage: req.URL.Path, StatusCode: resp.StatusCode, Headers: resp.Header.Clone(),
		})
	})
}

func TestHTTPUpstreamObserverPreservesWireCompressionHeaders(t *testing.T) {
	for _, protocol := range []string{"http1", "http2", "tls-fingerprint"} {
		fingerprint := protocol == "tls-fingerprint"
		for _, acceptEncoding := range []string{"", "gzip", "br"} {
			t.Run(protocol+"/encoding="+acceptEncoding, func(t *testing.T) {
				const body = "decoded response body"
				wireBody := diagnosticsGzip(t, body)
				encoding := "gzip"
				if acceptEncoding == "br" {
					var compressed bytes.Buffer
					writer := brotli.NewWriter(&compressed)
					_, err := io.WriteString(writer, body)
					require.NoError(t, err)
					require.NoError(t, writer.Close())
					wireBody, encoding = compressed.Bytes(), "br"
				}
				receivedHeaders := make(chan http.Header, 1)
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					receivedHeaders <- req.Header.Clone()
					w.Header().Set("Content-Encoding", encoding)
					w.Header().Set("Content-Length", strconv.Itoa(len(wireBody)))
					w.Header()["Set-Cookie"] = []string{"a=1", "b=2"}
					w.Header().Set("X-Long", strings.Repeat("x", 16000))
					_, _ = w.Write(wireBody)
				}))
				server.EnableHTTP2 = protocol == "http2"
				server.StartTLS()
				defer server.Close()
				upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
				var entry *upstreamClientEntry
				var err error
				profile := &tlsfingerprint.Profile{Name: "diagnostics-test"}
				if fingerprint {
					entry, err = upstream.getClientEntryWithTLS("", 41, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
				} else {
					entry, err = upstream.getClientEntry("", 41, 1, service.HTTPUpstreamProfileDefault, false, false)
				}
				require.NoError(t, err)
				transport := entry.client.Transport.(*http.Transport)
				defer transport.CloseIdleConnections()
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				localTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
				if fingerprint {
					// Exercise the DoWithTLS/profile cache path using a dialer that
					// trusts this local certificate instead of system certificates.
					transport.DialTLSContext = (&tls.Dialer{Config: localTLS}).DialContext
				} else {
					transport.TLSClientConfig = localTLS
					if protocol == "http2" {
						transport.ForceAttemptHTTP2 = true
						_, err = enableHTTP2KeepAlive(transport)
						require.NoError(t, err)
					}
				}
				var events []service.AccountTestUpstreamResponse
				req, err := http.NewRequestWithContext(diagnosticsContext(&events), http.MethodPost, server.URL+"/responses?key=private", nil)
				require.NoError(t, err)
				if acceptEncoding != "" {
					req.Header.Set("Accept-Encoding", acceptEncoding)
				}
				var resp *http.Response
				if fingerprint {
					resp, err = upstream.DoWithTLS(req, "", 41, 1, profile)
				} else {
					resp, err = upstream.Do(req, "", 41, 1)
				}
				require.NoError(t, err)
				defer resp.Body.Close()
				if protocol == "http2" {
					require.Equal(t, 2, resp.ProtoMajor)
				}
				decoded, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, body, string(decoded))
				require.Equal(t, acceptEncoding == "", resp.Uncompressed)
				require.Empty(t, resp.Header.Get("Content-Encoding"))
				require.Empty(t, resp.Header.Get("Content-Length"))
				require.Len(t, events, 1)
				require.Equal(t, encoding, events[0].Headers.Get("Content-Encoding"))
				require.Equal(t, strconv.Itoa(len(wireBody)), events[0].Headers.Get("Content-Length"))
				require.Equal(t, []string{"a=1", "b=2"}, events[0].Headers.Values("Set-Cookie"))
				require.Len(t, events[0].Headers.Get("X-Long"), 16000)
				require.Equal(t, "/responses", events[0].Stage)
				require.Equal(t, acceptEncoding, req.Header.Get("Accept-Encoding"), "caller headers must not be mutated")
				require.Same(t, transport, entry.client.Transport, "cached transport must not be replaced")
				require.False(t, transport.DisableCompression)
				wantEncoding := acceptEncoding
				if wantEncoding == "" {
					wantEncoding = "gzip"
				}
				require.Equal(t, wantEncoding, (<-receivedHeaders).Get("Accept-Encoding"))
				resp.Header.Set("Set-Cookie", "changed")
				require.Equal(t, []string{"a=1", "b=2"}, events[0].Headers.Values("Set-Cookie"))
				require.NoError(t, resp.Body.Close())
				require.Zero(t, atomic.LoadInt64(&entry.inFlight))
			})
		}
	}
}

func TestHTTPUpstreamObserverRecordsEveryGrokFallbackResponse(t *testing.T) {
	for _, fallbackStatus := range []int{200, 403, 502, 0} {
		t.Run(strconv.Itoa(fallbackStatus), func(t *testing.T) {
			upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
			entry, err := upstream.getClientEntry("", 41, 1, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			wireBody := diagnosticsGzip(t, `{"error":"Access denied"}`)
			calls := 0
			entry.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					require.Equal(t, grokCLIProxyHost, req.URL.Hostname())
					return &http.Response{StatusCode: 403, Request: req,
						Header: http.Header{"Content-Encoding": {"gzip"}, "Content-Length": {strconv.Itoa(len(wireBody))}, "X-Request-Id": {"first"}},
						Body:   io.NopCloser(bytes.NewReader(wireBody))}, nil
				}
				require.Equal(t, grokOfficialAPIHost, req.URL.Hostname())
				if fallbackStatus == 0 {
					return nil, errors.New("connection failed")
				}
				return &http.Response{StatusCode: fallbackStatus, Request: req,
					Header: http.Header{"X-Request-Id": {"fallback"}}, Body: io.NopCloser(strings.NewReader("fallback body"))}, nil
			})
			var events []service.AccountTestUpstreamResponse
			req, err := http.NewRequestWithContext(diagnosticsContext(&events), http.MethodPost,
				"https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader(`{"input":"hi"}`))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer test-token")
			resp, err := upstream.Do(req, "", 41, 1)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, 2, calls, "compressed access-denied body must still trigger fallback")
			if fallbackStatus == 200 {
				require.Equal(t, 200, resp.StatusCode)
				require.Equal(t, "fallback body", string(body))
			} else {
				require.Equal(t, 403, resp.StatusCode, "failed fallback must return the original response")
				require.JSONEq(t, `{"error":"Access denied"}`, string(body))
			}
			if fallbackStatus == 0 {
				require.Len(t, events, 1, "no invented response for a network error")
			} else {
				require.Len(t, events, 2, "the returned original response must not be recorded again")
				require.Equal(t, fallbackStatus, events[1].StatusCode)
				require.Equal(t, "fallback", events[1].Headers.Get("X-Request-Id"))
			}
			require.Equal(t, 403, events[0].StatusCode)
			require.Equal(t, "gzip", events[0].Headers.Get("Content-Encoding"))
			require.Equal(t, strconv.Itoa(len(wireBody)), events[0].Headers.Get("Content-Length"))
			require.NoError(t, resp.Body.Close())
			require.Zero(t, atomic.LoadInt64(&entry.inFlight))
		})
	}
}

func TestHTTPUpstreamObserverRedirectsAndRepeatedRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Path", req.URL.Path)
		if req.URL.Path == "/start" {
			http.Redirect(w, req, "/final?secret=hidden", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
	var events []service.AccountTestUpstreamResponse
	ctx := diagnosticsContext(&events)
	for range 2 {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/start", strings.NewReader("body"))
		require.NoError(t, err)
		resp, err := upstream.DoWithTLS(req, "", 41, 1, nil)
		require.NoError(t, err)
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	require.Len(t, events, 4)
	for i := 0; i < len(events); i += 2 {
		require.Equal(t, http.MethodPost, events[i].Method)
		require.Equal(t, "/start", events[i].Stage)
		require.Equal(t, 302, events[i].StatusCode)
		require.Equal(t, http.MethodGet, events[i+1].Method)
		require.Equal(t, "/final", events[i+1].Stage)
		require.Equal(t, 200, events[i+1].StatusCode)
	}
}

func TestHTTPUpstreamObserverPreservesEncodingNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name               string
		method             string
		acceptEncoding     string
		rangeHeader        string
		disableCompression bool
	}{
		{name: "head", method: http.MethodHead},
		{name: "range", method: http.MethodGet, rangeHeader: "bytes=0-3"},
		{name: "identity", method: http.MethodGet, acceptEncoding: "identity"},
		{name: "compression disabled", method: http.MethodGet, disableCompression: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receivedHeaders := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				receivedHeaders <- req.Header.Clone()
				_, _ = io.WriteString(w, "plain")
			}))
			defer server.Close()
			upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
			entry, err := upstream.getClientEntry("", 41, 1, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			transport := entry.client.Transport.(*http.Transport)
			transport.DisableCompression = tc.disableCompression
			defer transport.CloseIdleConnections()
			var events []service.AccountTestUpstreamResponse
			req, err := http.NewRequestWithContext(diagnosticsContext(&events), tc.method, server.URL, nil)
			require.NoError(t, err)
			req.Header.Set("Accept-Encoding", tc.acceptEncoding)
			req.Header.Set("Range", tc.rangeHeader)
			resp, err := upstream.Do(req, "", 41, 1)
			require.NoError(t, err)
			_, err = io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.False(t, resp.Uncompressed)
			require.Len(t, events, 1)
			headers := <-receivedHeaders
			require.Equal(t, tc.acceptEncoding, headers.Get("Accept-Encoding"))
			require.Equal(t, tc.rangeHeader, headers.Get("Range"))
		})
	}
}

func TestHTTPUpstreamObserverKeepsInvalidGzipAsBodyReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = io.WriteString(w, "invalid gzip body")
	}))
	defer server.Close()
	upstream := NewHTTPUpstream(nil)
	var events []service.AccountTestUpstreamResponse
	req, err := http.NewRequestWithContext(diagnosticsContext(&events), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := upstream.Do(req, "", 41, 1)
	require.NoError(t, err, "gzip errors should not replace the HTTP response")
	require.Len(t, events, 1)
	require.Equal(t, "gzip", events[0].Headers.Get("Content-Encoding"))
	require.True(t, resp.Uncompressed)
	_, err = io.ReadAll(resp.Body)
	require.ErrorIs(t, err, gzip.ErrHeader)
	require.NoError(t, resp.Body.Close())
}

func TestHTTPUpstreamObserverLeavesUnobservedClientAlone(t *testing.T) {
	client := &http.Client{Transport: &http.Transport{}}
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	require.Same(t, client, httpClientWithResponseObserver(client, req))
}

func TestHTTPUpstreamObserverDoesNotInventNetworkResponses(t *testing.T) {
	upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
	entry, err := upstream.getClientEntry("", 41, 1, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	entry.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection failed")
	})
	var events []service.AccountTestUpstreamResponse
	req, err := http.NewRequestWithContext(diagnosticsContext(&events), http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	resp, err := upstream.Do(req, "", 41, 1)
	require.ErrorContains(t, err, "connection failed")
	require.Nil(t, resp)
	require.Empty(t, events)
	require.Zero(t, atomic.LoadInt64(&entry.inFlight))
}
