package service

import (
	"context"
	"net/http"
)

// HTTPUpstreamResponseObserver runs synchronously for each upstream response,
// before retries, redirects or decompression consume or change it. The response
// is borrowed: observers must copy headers they retain and must not read its body.
type HTTPUpstreamResponseObserver func(*http.Request, *http.Response)

type httpUpstreamResponseObserverKey struct{}

func WithHTTPUpstreamResponseObserver(ctx context.Context, observer HTTPUpstreamResponseObserver) context.Context {
	return context.WithValue(ctx, httpUpstreamResponseObserverKey{}, observer)
}

func HTTPUpstreamResponseObserverFromContext(ctx context.Context) HTTPUpstreamResponseObserver {
	observer, _ := ctx.Value(httpUpstreamResponseObserverKey{}).(HTTPUpstreamResponseObserver)
	return observer
}

// ObserveHTTPUpstreamResponse reports an actual response, never a connection error
// without one. Each transport owns notification; callers must not report it again.
func ObserveHTTPUpstreamResponse(req *http.Request, resp *http.Response) {
	if resp == nil {
		return
	}
	if observer := HTTPUpstreamResponseObserverFromContext(req.Context()); observer != nil {
		observer(req, resp)
	}
}
