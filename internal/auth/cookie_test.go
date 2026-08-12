package auth

import "testing"

// The session cookie is shell access. Its Secure flag must not depend on OIDC
// happening to be configured with an https redirect: any mode without a redirect
// URL would otherwise send it in clear, and SameSite=Lax weakens with it.
func TestCookieSecureDecision(t *testing.T) {
	tests := []struct {
		name      string
		redirect  string
		secureEnv string
		insecure  string
		tlsCert   string
		want      bool
		wantErr   bool
	}{
		{name: "https redirect implies secure", redirect: "https://relay.example/auth/callback", want: true},
		{name: "relay serves TLS itself", tlsCert: "/etc/moony/cert.pem", want: true},
		{name: "explicitly requested", secureEnv: "1", want: true},
		{name: "no redirect, no TLS, no override is a startup error", wantErr: true},
		{name: "plain http allowed only with the explicit override", insecure: "1", want: false},
		{name: "override cannot silently beat an explicit secure request", secureEnv: "1", insecure: "1", want: true},
		{name: "http redirect alone is not enough", redirect: "http://relay.example/auth/callback", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MN_AUTH_COOKIE_SECURE", tc.secureEnv)
			t.Setenv("MN_AUTH_INSECURE_COOKIES", tc.insecure)
			t.Setenv("MN_RELAY_TLS_CERT", tc.tlsCert)

			got, err := cookieSecure(tc.redirect)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want a startup error rather than an insecure cookie by default")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("cookieSecure = %v, want %v", got, tc.want)
			}
		})
	}
}
