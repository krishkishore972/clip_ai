package auth

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/httpserver"
)

type Users interface { Resolve(context.Context, string, string) (Principal,error) }

func New(cfg config.Auth, users Users, logger *slog.Logger) func(http.Handler) http.Handler {
	client := jwks.NewClient(&clerk.ClientConfig{BackendConfig: clerk.BackendConfig{
		Key: clerk.String(cfg.SecretKey), HTTPClient: &http.Client{Timeout: 5*time.Second},
	}})
	return middleware(cfg, users, client, logger)
}

func middleware(cfg config.Auth, users Users, client *jwks.Client, logger *slog.Logger) func(http.Handler) http.Handler {
	fail := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control","no-store")
		w.Header().Set("WWW-Authenticate", "Bearer")
		if err := httpserver.WriteError(w,r,http.StatusUnauthorized,"UNAUTHENTICATED","Sign in to continue.",nil); err != nil { logger.ErrorContext(r.Context(),"write authentication error failed") }
	}
	return func(next http.Handler) http.Handler {
		verified := clerkhttp.WithHeaderAuthorization(
			clerkhttp.JWKSClient(client),
			clerkhttp.AuthorizationFailureHandler(http.HandlerFunc(fail)),
			clerkhttp.AuthorizedParty(func(party string) bool { return party == "" || slices.Contains(cfg.AuthorizedParties, party) }),
		)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := clerk.SessionClaimsFromContext(r.Context())
			if !ok || claims == nil || claims.Issuer != cfg.Issuer || claims.Subject == "" || claims.SessionID == "" || claims.Expiry == nil ||
				(cfg.Audience != "" && !slices.Contains(claims.Audience,cfg.Audience)) {
				fail(w,r); return
			}
			ctx, cancel := context.WithTimeout(r.Context(),5*time.Second)
			p, err := users.Resolve(ctx,claims.Issuer,claims.Subject)
			cancel()
			if err != nil {
				logger.ErrorContext(r.Context(),"resolve authenticated user failed", "trace_id",httpserver.TraceIDFromContext(r.Context()))
				_ = httpserver.WriteError(w,r,http.StatusServiceUnavailable,"identity_unavailable","User account is temporarily unavailable.",nil)
				return
			}
			next.ServeHTTP(w,r.WithContext(WithPrincipal(r.Context(),p)))
		}))
		return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
			w.Header().Set("Cache-Control","no-store")
			parts := strings.Fields(r.Header.Get("Authorization"))
			if len(r.Header.Values("Authorization")) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0],"Bearer") || len(parts[1]) > 16384 {
				fail(w,r); return
			}
			// Restrict algorithms before asking Clerk to look up a signing key.
			if _, err := josejwt.ParseSigned(parts[1],[]jose.SignatureAlgorithm{jose.RS256}); err != nil { fail(w,r); return }
			r = r.Clone(r.Context())
			r.Header.Set("Authorization","Bearer "+parts[1])
			verified.ServeHTTP(w,r)
		})
	}
}

func Me(w http.ResponseWriter,r *http.Request) {
	p, ok := FromContext(r.Context())
	if !ok { _ = httpserver.WriteError(w,r,http.StatusUnauthorized,"UNAUTHENTICATED","Sign in to continue.",nil); return }
	_ = httpserver.WriteJSON(w,http.StatusOK,p)
}
