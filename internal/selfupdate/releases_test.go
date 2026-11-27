package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func releaseResponse(status int, location, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Location": []string{location}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReleasesLatest(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		location string
		want     string
	}{
		{"relative redirect", http.StatusFound, "/9star-academy/servctl/releases/tag/v0.2.0", "v0.2.0"},
		{"absolute redirect", http.StatusFound, "https://github.com/9star-academy/servctl/releases/tag/v0.2.0", "v0.2.0"},
		{"foreign origin", http.StatusFound, "https://example.com/9star-academy/servctl/releases/tag/v0.2.0", ""},
		{"wrong repository", http.StatusFound, "/other/servctl/releases/tag/v0.2.0", ""},
		{"no releases", http.StatusFound, "/9star-academy/servctl/releases", ""},
		{"nested tag", http.StatusFound, "/9star-academy/servctl/releases/tag/v0.2.0%2Fother", ""},
		{"not found", http.StatusNotFound, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newReleases()
			r.client.Transport = releaseTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != r.repo+"/releases/latest" {
					t.Fatalf("unexpected request %s", req.URL)
				}
				return releaseResponse(tt.status, tt.location, ""), nil
			})
			got, err := r.latest(context.Background())
			if got != tt.want || (err == nil) != (tt.want != "") {
				t.Fatalf("latest = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestReleasesDownload(t *testing.T) {
	const version = "v0.2.0"
	const archive = "servctl_linux_arm64.tar.gz"
	const body = "compressed file fixture"
	entry := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(body)), archive)
	for _, tt := range []struct {
		name     string
		manifest string
		asset    string
		redirect string
		status   int
		wantOK   bool
	}{
		{"valid release", entry, body, "", http.StatusOK, true},
		{"HTTPS redirect", entry, body, "https://release-assets.githubusercontent.com/file", http.StatusOK, true},
		{"HTTP redirect", entry, body, "http://release-assets.githubusercontent.com/file", http.StatusOK, false},
		{"checksum mismatch", entry, "corrupted fixture", "", http.StatusOK, false},
		{"duplicate checksum", entry + entry, body, "", http.StatusOK, false},
		{"missing checksum", "", body, "", http.StatusOK, false},
		{"invalid checksum", "not-a-digest  " + archive, body, "", http.StatusOK, false},
		{"oversized manifest", strings.Repeat("x", 65537), body, "", http.StatusOK, false},
		{"asset not found", entry, body, "", http.StatusNotFound, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newReleases()
			base := r.repo + "/releases/download/" + version + "/"
			r.client.Transport = releaseTransport(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case base + "checksums.txt":
					return releaseResponse(http.StatusOK, "", tt.manifest), nil
				case base + archive:
					if tt.redirect == "" {
						return releaseResponse(tt.status, "", tt.asset), nil
					}
					return releaseResponse(http.StatusFound, tt.redirect, ""), nil
				case "https://release-assets.githubusercontent.com/file":
					return releaseResponse(tt.status, "", tt.asset), nil
				default:
					t.Fatalf("unexpected request %s", req.URL)
					return nil, nil
				}
			})
			got, err := r.download(context.Background(), version, "arm64")
			if (err == nil) != tt.wantOK || (tt.wantOK && !bytes.Equal(got, []byte(body))) {
				t.Fatalf("download = %q, %v", got, err)
			}
		})
	}
}

func TestResolveRepoURL(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want string
	}{
		{"", defaultRepoURL},
		{"  ", defaultRepoURL},
		{"https://mirror.example.com/9star-academy/servctl/", "https://mirror.example.com/9star-academy/servctl"},
		{"http://mirror.example.com/9star-academy/servctl", ""},
		{"https://user:pass@mirror.example.com/repo", ""},
		{"https://mirror.example.com/repo?x=1", ""},
	} {
		got, err := resolveRepoURL(tt.raw)
		if got != tt.want || (err == nil) != (tt.want != "") {
			t.Errorf("resolveRepoURL(%q) = %q, %v; want %q", tt.raw, got, err, tt.want)
		}
	}
}
