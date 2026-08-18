package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type authorizationFixture struct {
	server         *httptest.Server
	tokenExchanges atomic.Int32
}

func newAuthorizationFixture() *authorizationFixture {
	fixture := &authorizationFixture{}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(&oauthex.ProtectedResourceMetadata{
				Resource:             fixture.server.URL + "/mcp",
				AuthorizationServers: []string{fixture.server.URL},
				ScopesSupported:      []string{"mcp:read"},
			})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(&oauthex.AuthServerMeta{
				Issuer:                                     fixture.server.URL,
				AuthorizationEndpoint:                      fixture.server.URL + "/authorize",
				TokenEndpoint:                              fixture.server.URL + "/token",
				ResponseTypesSupported:                     []string{"code"},
				GrantTypesSupported:                        []string{"authorization_code"},
				TokenEndpointAuthMethodsSupported:          []string{"none"},
				CodeChallengeMethodsSupported:              []string{"S256"},
				AuthorizationResponseIssParameterSupported: true,
			})
		case "/token":
			fixture.tokenExchanges.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "fixture-access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	return fixture
}

func (f *authorizationFixture) close() {
	f.server.Close()
}

func (f *authorizationFixture) issuer() string {
	return f.server.URL
}

func (f *authorizationFixture) tokenExchangeCount() int {
	return int(f.tokenExchanges.Load())
}

func authorize(ctx context.Context, fixture *authorizationFixture, callbackIssuer, credentialIssuer string) error {
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL: "http://127.0.0.1/callback",
		PreregisteredClient: &oauthex.ClientCredentials{
			ClientID: "public-example-client",
			Issuer:   credentialIssuer,
		},
		Client: fixture.server.Client(),
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			authorizationURL, err := url.Parse(args.URL)
			if err != nil {
				return nil, err
			}
			return &auth.AuthorizationResult{
				Code:  "fixture-code",
				State: authorizationURL.Query().Get("state"),
				Iss:   callbackIssuer,
			}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("create authorization handler: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture.server.URL+"/mcp", nil)
	if err != nil {
		return err
	}
	response := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header: http.Header{
			"Www-Authenticate": []string{fmt.Sprintf(
				`Bearer resource_metadata=%q, scope="mcp:read"`,
				fixture.server.URL+"/.well-known/oauth-protected-resource/mcp",
			)},
		},
		Body: io.NopCloser(strings.NewReader("authorization required")),
	}
	return handler.Authorize(ctx, request, response)
}

type credentialStore struct {
	mu      sync.RWMutex
	entries map[string]oauthex.ClientCredentials
}

func newCredentialStore() *credentialStore {
	return &credentialStore{entries: make(map[string]oauthex.ClientCredentials)}
}

func credentialIssuerKey(issuer string) string {
	return strings.TrimSuffix(issuer, "/")
}

func (s *credentialStore) save(issuer string, credentials oauthex.ClientCredentials) {
	issuer = credentialIssuerKey(issuer)
	credentials.Issuer = issuer
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[issuer] = credentials
}

func (s *credentialStore) load(issuer string) (oauthex.ClientCredentials, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	credentials, ok := s.entries[credentialIssuerKey(issuer)]
	return credentials, ok
}

func inferDCRApplicationType(redirectURI, explicitType string) (string, error) {
	metadata := &oauthex.ClientRegistrationMetadata{
		RedirectURIs:    []string{redirectURI},
		ApplicationType: explicitType,
	}
	_, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirectURI,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: metadata,
		},
		AuthorizationCodeFetcher: unusedAuthorizationCodeFetcher,
	})
	return metadata.ApplicationType, err
}

func validateCIMDConfiguration(clientIDURL string) error {
	_, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL: "http://127.0.0.1/callback",
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{
			URL: clientIDURL,
		},
		AuthorizationCodeFetcher: unusedAuthorizationCodeFetcher,
	})
	return err
}

func unusedAuthorizationCodeFetcher(context.Context, *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	return nil, fmt.Errorf("authorization code fetcher is not used by configuration validation")
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}); err != nil {
		panic(err)
	}
}

func run(ctx context.Context, printf func(string, ...any)) error {
	fixture := newAuthorizationFixture()
	defer fixture.close()

	if err := authorize(ctx, fixture, fixture.issuer(), fixture.issuer()); err != nil {
		return fmt.Errorf("matching issuer authorization: %w", err)
	}
	printf("RFC 9207 matching issuer accepted: true")
	printf("token exchanges after valid response: %d", fixture.tokenExchangeCount())

	beforeMismatch := fixture.tokenExchangeCount()
	mismatchErr := authorize(ctx, fixture, "https://attacker.invalid", fixture.issuer())
	rejectedBeforeExchange := mismatchErr != nil && fixture.tokenExchangeCount() == beforeMismatch
	printf("RFC 9207 mismatched issuer rejected before token exchange: %t", rejectedBeforeExchange)

	store := newCredentialStore()
	store.save(fixture.issuer(), oauthex.ClientCredentials{ClientID: "issuer-bound-client"})
	_, sameIssuer := store.load(fixture.issuer() + "/")
	_, otherIssuer := store.load("https://other-issuer.invalid")
	printf("credential lookup: same-issuer=%t cross-issuer=%t", sameIssuer, otherIssuer)

	applicationType, err := inferDCRApplicationType("http://127.0.0.1:8080/callback", "")
	if err != nil {
		return fmt.Errorf("infer DCR application type: %w", err)
	}
	printf("DCR fallback application_type: %s", applicationType)

	cimdValid := validateCIMDConfiguration("https://client.example/oauth/client-metadata.json") == nil
	printf("CIMD non-root HTTPS configuration valid: %t", cimdValid)
	return nil
}
