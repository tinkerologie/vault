package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyAuth(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	tests := []struct {
		name       string
		apiKey     string
		header     string
		headerVal  string
		queryToken string
		wantStatus int
	}{
		{
			name:       "no key configured allows all",
			apiKey:     "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid bearer token",
			apiKey:     "test-secret-key",
			header:     "Authorization",
			headerVal:  "Bearer test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid x-api-key",
			apiKey:     "test-secret-key",
			header:     "X-API-Key",
			headerVal:  "test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid query token",
			apiKey:     "test-secret-key",
			queryToken: "test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing key returns 401",
			apiKey:     "test-secret-key",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong key returns 401",
			apiKey:     "test-secret-key",
			header:     "Authorization",
			headerVal:  "Bearer wrong-key",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong x-api-key returns 401",
			apiKey:     "test-secret-key",
			header:     "X-API-Key",
			headerVal:  "wrong-key",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mw := APIKeyAuth(func() string { return tt.apiKey })
			handler := mw(okHandler)

			path := "/"
			if tt.queryToken != "" {
				path = "/?token=" + tt.queryToken
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tt.header != "" {
				req.Header.Set(tt.header, tt.headerVal)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestLocalUIBypass(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	tests := []struct {
		name         string
		apiKey       string
		secFetchSite string
		authHeader   string
		wantStatus   int
	}{
		{
			name:         "same-origin bypasses auth",
			apiKey:       "test-secret-key",
			secFetchSite: "same-origin",
			wantStatus:   http.StatusOK,
		},
		{
			name:         "same-origin bypasses even without key configured",
			apiKey:       "",
			secFetchSite: "same-origin",
			wantStatus:   http.StatusOK,
		},
		{
			name:         "cross-site requires key",
			apiKey:       "test-secret-key",
			secFetchSite: "cross-site",
			wantStatus:   http.StatusUnauthorized,
		},
		{
			name:         "cross-site with valid key passes",
			apiKey:       "test-secret-key",
			secFetchSite: "cross-site",
			authHeader:   "Bearer test-secret-key",
			wantStatus:   http.StatusOK,
		},
		{
			name:       "no sec-fetch-site requires key",
			apiKey:     "test-secret-key",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no sec-fetch-site with valid key passes",
			apiKey:     "test-secret-key",
			authHeader: "Bearer test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:         "none origin requires key",
			apiKey:       "test-secret-key",
			secFetchSite: "none",
			wantStatus:   http.StatusUnauthorized,
		},
		{
			name:       "no key configured allows all regardless",
			apiKey:     "",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mw := LocalUIBypass(func() string { return tt.apiKey })
			handler := mw(okHandler)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.secFetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.secFetchSite)
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}
