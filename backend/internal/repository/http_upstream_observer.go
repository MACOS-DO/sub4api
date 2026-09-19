package repository

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/service"
)

// Install inside the Grok fallback transport so every attempt is observed, while
// sharing the cached base transport and leaving requests without an observer alone.
func httpClientWithResponseObserver(client *http.Client, req *http.Request) *http.Client {
	if req == nil || service.HTTPUpstreamResponseObserverFromContext(req.Context()) == nil {
		return client
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone := *client
	clone.Transport = &responseObservingTransport{base: base}
	return &clone
}

type responseObservingTransport struct {
	base http.RoundTripper
}

func (t *responseObservingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// net/http removes compression headers when it negotiates gzip itself. Make
	// the same negotiation explicit on a private request, then emulate its lazy
	// decoding after observation so fallback decisions still see decoded errors.
	requestGzip := req.Header.Get("Accept-Encoding") == "" && req.Header.Get("Range") == "" && req.Method != http.MethodHead
	if transport, ok := t.base.(*http.Transport); ok && transport.DisableCompression {
		requestGzip = false
	}
	if requestGzip {
		req = req.Clone(req.Context())
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("Accept-Encoding", "gzip")
	}
	resp, err := t.base.RoundTrip(req)
	service.ObserveHTTPUpstreamResponse(req, resp)
	if requestGzip && resp != nil && strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		resp.Body = &observedGzipBody{ReadCloser: resp.Body}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Uncompressed = true
	}
	return resp, err
}

// Like net/http's automatic gzip reader, defer decoding until the body is read.
// Invalid gzip remains a body-read error, and closing still closes the wire body.
type observedGzipBody struct {
	io.ReadCloser
	reader  *gzip.Reader
	initErr error
}

func (b *observedGzipBody) Read(p []byte) (int, error) {
	if b.reader == nil && b.initErr == nil {
		b.reader, b.initErr = gzip.NewReader(b.ReadCloser)
	}
	if b.initErr != nil {
		return 0, b.initErr
	}
	return b.reader.Read(p)
}
