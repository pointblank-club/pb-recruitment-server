package internal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfiguredCORSOrigins(t *testing.T) {
	for _, tt := range []struct {
		name, configured, origin string
		allowed                  bool
	}{
		{"configured origin", " https://recruitment-e2e.vercel.app , , https://preview.vercel.app ", "https://recruitment-e2e.vercel.app", true},
		{"second configured origin", "https://recruitment-e2e.vercel.app,https://preview.vercel.app", "https://preview.vercel.app", true},
		{"local origin retained", "https://recruitment-e2e.vercel.app", "http://localhost:5173", true},
		{"existing hosted origin retained", "", "https://recruitment.pointblank.club", true},
		{"unlisted preview denied", "https://recruitment-e2e.vercel.app", "https://other.vercel.app", false},
		{"lookalike denied", "https://recruitment-e2e.vercel.app", "https://recruitment-e2e.vercel.app.example.com", false},
		{"wildcard does not grant access", "https://*.vercel.app,*", "https://other.vercel.app", false},
		{"blank config does not grant access", " , , ", "https://other.vercel.app", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CORS_ALLOWED_ORIGINS", tt.configured)
			e := NewEchoServer(nil, nil)
			req := httptest.NewRequest(http.MethodOptions, "/users/profile", nil)
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			req.Header.Set("Access-Control-Request-Headers", "Authorization,Content-Type")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			want := ""
			if tt.allowed {
				want = tt.origin
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
				t.Fatalf("allowed origin = %q, want %q", got, want)
			}
			if tt.allowed && rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("allowed preflight missing credential support")
			}
		})
	}
}

func TestSwaggerGating(t *testing.T) {
	tests := []struct {
		name          string
		stage         string
		enableSwagger string
		wantMounted   bool
	}{
		{
			name:          "dev environment without flag",
			stage:         "dev",
			enableSwagger: "",
			wantMounted:   true,
		},
		{
			name:          "dev environment with flag",
			stage:         "dev",
			enableSwagger: "true",
			wantMounted:   true,
		},
		{
			name:          "production with enable flag",
			stage:         "production",
			enableSwagger: "true",
			wantMounted:   true,
		},
		{
			name:          "production without flag",
			stage:         "production",
			enableSwagger: "",
			wantMounted:   false,
		},
		{
			name:          "production with flag false",
			stage:         "production",
			enableSwagger: "false",
			wantMounted:   false,
		},
		{
			name:          "unset STAGE fails closed",
			stage:         "",
			enableSwagger: "",
			wantMounted:   false,
		},
		{
			name:          "mistyped STAGE fails closed",
			stage:         "staging",
			enableSwagger: "",
			wantMounted:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STAGE", tt.stage)
			t.Setenv("ENABLE_SWAGGER", tt.enableSwagger)

			e := NewEchoServer(nil, nil)

			mounted := false
			for _, route := range e.Routes() {
				if strings.HasPrefix(route.Path, "/swagger") {
					mounted = true
					break
				}
			}

			if mounted != tt.wantMounted {
				t.Errorf("Swagger mounted = %v, want %v (STAGE=%q, ENABLE_SWAGGER=%q)",
					mounted, tt.wantMounted, tt.stage, tt.enableSwagger)
			}
		})
	}
}
