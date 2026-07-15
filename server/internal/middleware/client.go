package middleware

import (
	"context"
	"net/http"
)

// Client metadata context keys.
//
// Populated by ClientMetadata middleware from X-Client-Platform / X-Client-Version /
// X-Client-OS request headers. Sent by every first-party client (Web, Desktop, CLI,
// Daemon) so the server can split logs / metrics / gating decisions by caller
// without having to reverse-engineer User-Agent strings or upgrade payloads.
//
// All three values are best-effort: handlers must treat missing values as
// "unknown" and never make security decisions based on them — these headers
// are client-controlled and trivial to spoof.
type clientMetadata struct {
	platform string
	version  string
	os       string
}

type clientMetadataKey struct{}

// Header names — exported so other packages (request logger, realtime hub)
// can stay in sync without re-declaring magic strings.
const (
	headerClientPlatform = "X-Client-Platform"
	headerClientVersion  = "X-Client-Version"
	headerClientOS       = "X-Client-OS"
)

// ClientMetadata extracts X-Client-Platform / X-Client-Version / X-Client-OS
// from the request and stashes them in the request context so downstream
// handlers and the request logger can read them via ClientMetadataFromContext.
//
// Wired in router.go before route mounting so every authenticated and
// unauthenticated handler benefits from the same observability dimensions.
func ClientMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := clientMetadata{
			platform: r.Header.Get(headerClientPlatform),
			version:  r.Header.Get(headerClientVersion),
			os:       r.Header.Get(headerClientOS),
		}
		if metadata == (clientMetadata{}) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientMetadataKey{}, metadata)))
	})
}

// ClientMetadataFromContext returns the platform/version/os captured from
// X-Client-* headers. Empty strings are returned for any value that wasn't
// sent — callers must treat missing values as "unknown" rather than failing.
func ClientMetadataFromContext(ctx context.Context) (platform, version, os string) {
	metadata, _ := ctx.Value(clientMetadataKey{}).(clientMetadata)
	return metadata.platform, metadata.version, metadata.os
}
