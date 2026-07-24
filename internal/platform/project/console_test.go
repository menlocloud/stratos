package project

import "testing"

// consoleBackend turns nova's noVNC page URL into the websockify target we reverse-proxy to.
func TestConsoleBackend(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{
			// The common nova shape: the socket path rides in the `path` query, url-encoded.
			name: "path query with leading ?",
			raw:  "https://cloud-console.menlo.ai:6080/vnc_lite.html?path=%3Ftoken%3De0cd42b5-800a-4144-9146-bb457eb356f2",
			want: "https://cloud-console.menlo.ai:6080/?token=e0cd42b5-800a-4144-9146-bb457eb356f2",
		},
		{
			// Some deployments put an explicit websockify path in front of the token.
			name: "path query with explicit websockify segment",
			raw:  "https://host:6080/vnc_auto.html?path=websockify%3Ftoken%3Dabc",
			want: "https://host:6080/websockify?token=abc",
		},
		{
			// No `path` blob — fall back to the bare token query.
			name: "bare token query",
			raw:  "http://host:6080/vnc.html?token=xyz",
			want: "http://host:6080/?token=xyz",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := consoleBackend(c.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got.String() != c.want {
				t.Fatalf("got %q want %q", got.String(), c.want)
			}
		})
	}

	if _, err := consoleBackend("::not a url::"); err == nil {
		t.Fatal("expected an error for an unparseable url")
	}
	if _, err := consoleBackend(""); err == nil {
		t.Fatal("expected an error for an empty url")
	}
}
