package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestListenAddr(t *testing.T) {
	tests := []struct {
		env     string
		want    string
		wantErr bool
	}{
		{env: "", want: defaultAddr},
		{env: "127.0.0.1:5000", want: "127.0.0.1:5000"},
		{env: "[::1]:5000", want: "[::1]:5000"},
		{env: "0.0.0.0:4747", wantErr: true},
		{env: ":4747", wantErr: true},
		{env: "localhost:4747", wantErr: true},
		{env: "192.168.1.10:4747", wantErr: true},
		{env: "4747", wantErr: true},
	}
	for _, tt := range tests {
		got, err := listenAddr(tt.env)
		if tt.wantErr {
			if err == nil {
				t.Errorf("listenAddr(%q) = %q, want error", tt.env, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("listenAddr(%q) = %q, %v; want %q", tt.env, got, err, tt.want)
		}
	}
}

func TestUIHandler(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":    {Data: []byte("<html>shell</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := uiHandler(dist)

	tests := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{path: "/", wantCode: http.StatusOK, wantBody: "shell"},
		{path: "/assets/app.js", wantCode: http.StatusOK, wantBody: "console.log"},
		{path: "/projects/acme-shop", wantCode: http.StatusOK, wantBody: "shell"},
		{path: "/assets/missing.js", wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != tt.wantCode {
			t.Errorf("GET %s: code %d, want %d", tt.path, rec.Code, tt.wantCode)
		}
		if tt.wantBody != "" && !strings.Contains(string(body), tt.wantBody) {
			t.Errorf("GET %s: body %q, want it to contain %q", tt.path, body, tt.wantBody)
		}
	}
}

func TestUIHandlerNotBuilt(t *testing.T) {
	h := uiHandler(fstest.MapFS{".gitkeep": {}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "task build") {
		t.Errorf("got %d %q, want the not-built page", rec.Code, rec.Body.String())
	}
}
