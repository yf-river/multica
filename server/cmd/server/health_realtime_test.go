package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealtimeMetricsHandlerAccess(t *testing.T) {
	const token = "secret-test-token"
	tests := []struct {
		name          string
		configured    string
		remote        string
		header        string
		value         string
		wantStatus    int
		wantChallenge bool
		wantJSON      bool
	}{
		{name: "token missing", configured: token, remote: "203.0.113.10:54321", wantStatus: http.StatusUnauthorized, wantChallenge: true},
		{name: "token required on loopback", configured: token, remote: "127.0.0.1:1234", wantStatus: http.StatusUnauthorized},
		{name: "wrong token", configured: token, remote: "203.0.113.10:54321", header: "Authorization", value: "Bearer not-the-token", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", configured: token, remote: "203.0.113.10:54321", header: "Authorization", value: "Basic " + token, wantStatus: http.StatusUnauthorized},
		{name: "valid token", configured: token, remote: "203.0.113.10:54321", header: "Authorization", value: "Bearer " + token, wantStatus: http.StatusOK, wantJSON: true},
		{name: "loopback ipv4", remote: "127.0.0.1:9999", wantStatus: http.StatusOK},
		{name: "loopback ipv6", remote: "[::1]:9999", wantStatus: http.StatusOK},
		{name: "non-loopback ipv4", remote: "10.0.0.5:1234", wantStatus: http.StatusNotFound},
		{name: "non-loopback ipv6", remote: "[2001:db8::1]:1234", wantStatus: http.StatusNotFound},
		{name: "forwarded for", remote: "127.0.0.1:1234", header: "X-Forwarded-For", value: "203.0.113.10", wantStatus: http.StatusNotFound},
		{name: "forwarded for chain", remote: "127.0.0.1:1234", header: "X-Forwarded-For", value: "203.0.113.10, 10.0.0.1", wantStatus: http.StatusNotFound},
		{name: "real IP", remote: "127.0.0.1:1234", header: "X-Real-Ip", value: "203.0.113.10", wantStatus: http.StatusNotFound},
		{name: "forwarded host", remote: "127.0.0.1:1234", header: "X-Forwarded-Host", value: "metrics.example.com", wantStatus: http.StatusNotFound},
		{name: "forwarded proto", remote: "127.0.0.1:1234", header: "X-Forwarded-Proto", value: "https", wantStatus: http.StatusNotFound},
		{name: "forwarded RFC7239", remote: "127.0.0.1:1234", header: "Forwarded", value: "for=203.0.113.10;proto=https", wantStatus: http.StatusNotFound},
		{name: "forwarded ipv6 loopback", remote: "[::1]:9999", header: "X-Forwarded-For", value: "203.0.113.10", wantStatus: http.StatusNotFound},
		{name: "blank forwarding header", remote: "127.0.0.1:9999", header: "X-Forwarded-For", value: "   ", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health/realtime", nil)
			req.RemoteAddr = tt.remote
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}
			rec := httptest.NewRecorder()
			realtimeMetricsHandler(tt.configured).ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantChallenge && rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate header")
			}
			if tt.wantJSON && rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
			}
		})
	}
}
