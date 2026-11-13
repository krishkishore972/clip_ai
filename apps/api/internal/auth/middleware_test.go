package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/krishkishore972/clip_ai/internal/config"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeUsers struct {
	calls int
	fail  bool
}

func (u *fakeUsers) Resolve(_ context.Context, issuer, subject string) (Principal, error) {
	u.calls++
	if u.fail {
		return Principal{}, errors.New("sensitive database details")
	}
	return Principal{UserID: "internal-user", WorkspaceID: "personal-workspace"}, nil
}

func TestClerkAuthentication(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid, nextKid := newID(), newID()
	raw, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"},
		{Key: &other.PublicKey, KeyID: nextKid, Algorithm: "RS256", Use: "sig"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	client := jwks.NewClient(&clerk.ClientConfig{BackendConfig: clerk.BackendConfig{
		Key: clerk.String("sk_test_not_real"), HTTPClient: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			if r.URL.Path != "/v1/jwks" {
				t.Errorf("unexpected Clerk path: %s", r.URL.Path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
		})},
	}})
	cfg := config.Auth{Issuer: "https://test.clerk.accounts.dev", Audience: "clip-api", AuthorizedParties: []string{"https://trusted.example"}}
	users := &fakeUsers{}
	var logs bytes.Buffer
	h := middleware(cfg, users, client, slog.New(slog.NewTextHandler(&logs, nil)))(http.HandlerFunc(Me))
	sign := func(private *rsa.PrivateKey, id string, changes map[string]any) string {
		t.Helper()
		claims := map[string]any{"iss": cfg.Issuer, "sub": "user_test", "sid": "sess_test", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Add(-time.Second).Unix(), "aud": "clip-api"}
		for k, v := range changes {
			if v == nil {
				delete(claims, k)
			} else {
				claims[k] = v
			}
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: private}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", id))
		if err != nil {
			t.Fatal(err)
		}
		token, err := josejwt.Signed(signer).Claims(claims).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, tc := range []struct {
		name, header string
		want         int
	}{
		{"missing", "", 401},
		{"malformed", "Bearer not-a-jwt", 401},
		{"wrong scheme", "Basic abc", 401},
		{"valid native", "Bearer " + sign(key, kid, nil), 200},
		{"allowed party", "Bearer " + sign(key, kid, map[string]any{"azp": "https://trusted.example"}), 200},
		{"expired", "Bearer " + sign(key, kid, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}), 401},
		{"not yet valid", "Bearer " + sign(key, kid, map[string]any{"nbf": time.Now().Add(time.Hour).Unix()}), 401},
		{"missing expiry", "Bearer " + sign(key, kid, map[string]any{"exp": nil}), 401},
		{"missing session", "Bearer " + sign(key, kid, map[string]any{"sid": nil}), 401},
		{"pending session", "Bearer " + sign(key, kid, map[string]any{"sts": "pending"}), 401},
		{"wrong issuer", "Bearer " + sign(key, kid, map[string]any{"iss": "https://other.clerk.accounts.dev"}), 401},
		{"wrong audience", "Bearer " + sign(key, kid, map[string]any{"aud": "other-api"}), 401},
		{"missing audience", "Bearer " + sign(key, kid, map[string]any{"aud": nil}), 401},
		{"wrong party", "Bearer " + sign(key, kid, map[string]any{"azp": "https://evil.example"}), 401},
		{"wrong signature", "Bearer " + sign(other, kid, nil), 401},
		{"new signing key", "Bearer " + sign(other, nextKid, nil), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := users.calls
			r := httptest.NewRequest("GET", "/me", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 401 && (users.calls != before || !strings.Contains(w.Body.String(), "UNAUTHENTICATED")) {
				t.Fatal("invalid identity reached persistence or wrong error envelope")
			}
			if tc.want == 200 && !strings.Contains(w.Body.String(), `"workspaceId":"personal-workspace"`) {
				t.Fatal("/me did not return internal identity")
			}
		})
	}
	if requests.Load() != 2 {
		t.Fatalf("expected cached key plus refresh for new kid, got %d JWKS requests", requests.Load())
	}
	mac, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: bytes.Repeat([]byte("a"), 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	macToken, err := josejwt.Signed(mac).Claims(map[string]any{"sub": "user_test"}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	badAlg := httptest.NewRequest("GET", "/me", nil)
	badAlg.Header.Set("Authorization", "Bearer "+macToken)
	badResult := httptest.NewRecorder()
	h.ServeHTTP(badResult, badAlg)
	if badResult.Code != 401 {
		t.Fatal("non-RS256 algorithm accepted")
	}
	r := httptest.NewRequest("GET", "/me", nil)
	r.Header.Set("Cookie", "__session="+sign(key, kid, nil))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("cookie must not authenticate the mobile bearer API")
	}
	r.Header.Set("Authorization", "Bearer "+sign(key, kid, nil))
	r.Header.Add("Authorization", "Bearer duplicate")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("duplicate authorization header accepted")
	}
	users.fail = true
	r.Header.Set("Authorization", "Bearer "+sign(key, kid, nil))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String()+logs.String(), "sensitive database details") {
		t.Fatal("identity persistence failure not safely handled")
	}
}
