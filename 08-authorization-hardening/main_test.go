package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func TestAuthorizeValidatesRFC9207Issuer(t *testing.T) {
	tests := []struct {
		name           string
		callbackIssuer func(*authorizationFixture) string
		wantError      bool
		wantExchanges  int
	}{
		{
			name: "matching issuer",
			callbackIssuer: func(f *authorizationFixture) string {
				return f.issuer()
			},
			wantExchanges: 1,
		},
		{
			name: "missing issuer",
			callbackIssuer: func(*authorizationFixture) string {
				return ""
			},
			wantError: true,
		},
		{
			name: "mismatched issuer",
			callbackIssuer: func(*authorizationFixture) string {
				return "https://attacker.invalid"
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorizationFixture()
			defer fixture.close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := authorize(ctx, fixture, test.callbackIssuer(fixture), fixture.issuer())
			if (err != nil) != test.wantError {
				t.Fatalf("authorize() error = %v, wantError %t", err, test.wantError)
			}
			if got := fixture.tokenExchangeCount(); got != test.wantExchanges {
				t.Fatalf("token exchanges = %d, want %d", got, test.wantExchanges)
			}
		})
	}
}

func TestPreregisteredCredentialsAreBoundToIssuer(t *testing.T) {
	tests := []struct {
		name             string
		credentialIssuer func(*authorizationFixture) string
		wantError        bool
		wantExchanges    int
	}{
		{
			name: "matching issuer",
			credentialIssuer: func(f *authorizationFixture) string {
				return f.issuer()
			},
			wantExchanges: 1,
		},
		{
			name: "single trailing slash is equivalent",
			credentialIssuer: func(f *authorizationFixture) string {
				return f.issuer() + "/"
			},
			wantExchanges: 1,
		},
		{
			name: "different issuer",
			credentialIssuer: func(*authorizationFixture) string {
				return "https://other-issuer.invalid"
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorizationFixture()
			defer fixture.close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := authorize(ctx, fixture, fixture.issuer(), test.credentialIssuer(fixture))
			if (err != nil) != test.wantError {
				t.Fatalf("authorize() error = %v, wantError %t", err, test.wantError)
			}
			if got := fixture.tokenExchangeCount(); got != test.wantExchanges {
				t.Fatalf("token exchanges = %d, want %d", got, test.wantExchanges)
			}
		})
	}
}

func TestCredentialStoreKeysByIssuer(t *testing.T) {
	store := newCredentialStore()
	store.save("https://issuer-a.example/", oauthex.ClientCredentials{ClientID: "client-a"})

	credentials, ok := store.load("https://issuer-a.example")
	if !ok {
		t.Fatal("same issuer lookup failed")
	}
	if credentials.ClientID != "client-a" {
		t.Fatalf("ClientID = %q, want client-a", credentials.ClientID)
	}
	if credentials.Issuer != "https://issuer-a.example" {
		t.Fatalf("stored issuer = %q, want canonical issuer", credentials.Issuer)
	}
	if _, ok := store.load("https://issuer-b.example"); ok {
		t.Fatal("issuer B unexpectedly loaded issuer A credentials")
	}
}

func TestDCRApplicationTypeInference(t *testing.T) {
	tests := []struct {
		name         string
		redirectURI  string
		explicitType string
		wantType     string
		wantError    bool
	}{
		{
			name:        "loopback is native",
			redirectURI: "http://127.0.0.1:8080/callback",
			wantType:    "native",
		},
		{
			name:        "custom scheme is native",
			redirectURI: "example-app://oauth/callback",
			wantType:    "native",
		},
		{
			name:        "remote HTTPS is web",
			redirectURI: "https://client.example/oauth/callback",
			wantType:    "web",
		},
		{
			name:         "conflicting explicit type is rejected",
			redirectURI:  "http://localhost:8080/callback",
			explicitType: "web",
			wantType:     "web",
			wantError:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotType, err := inferDCRApplicationType(test.redirectURI, test.explicitType)
			if (err != nil) != test.wantError {
				t.Fatalf("inferDCRApplicationType() error = %v, wantError %t", err, test.wantError)
			}
			if gotType != test.wantType {
				t.Fatalf("application_type = %q, want %q", gotType, test.wantType)
			}
		})
	}
}

func TestCIMDRequiresNonRootHTTPSURL(t *testing.T) {
	tests := []struct {
		name      string
		clientID  string
		wantError bool
	}{
		{
			name:     "HTTPS URL with path",
			clientID: "https://client.example/oauth/client-metadata.json",
		},
		{
			name:      "HTTPS URL without path",
			clientID:  "https://client.example",
			wantError: true,
		},
		{
			name:      "HTTP URL",
			clientID:  "http://client.example/oauth/client-metadata.json",
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCIMDConfiguration(test.clientID)
			if (err != nil) != test.wantError {
				t.Fatalf("validateCIMDConfiguration() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

func TestRunShowsAuthorizationBoundaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []string
	if err := run(ctx, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"RFC 9207 matching issuer accepted: true",
		"token exchanges after valid response: 1",
		"RFC 9207 mismatched issuer rejected before token exchange: true",
		"credential lookup: same-issuer=true cross-issuer=false",
		"DCR fallback application_type: native",
		"CIMD non-root HTTPS configuration valid: true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"fixture-access-token", "fixture-code"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("output leaked fixture credential %q", forbidden)
		}
	}
}
