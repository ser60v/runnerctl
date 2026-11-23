package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const releaseCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const otherCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func testReleaseRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("0.1.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".build", "release")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"SHA256SUMS",
		"runnerctl_0.1.0_linux_amd64.tar.gz",
		"runnerctl_0.1.0_linux_arm64.tar.gz",
		"runnerctl_0.1.0_darwin_amd64.tar.gz",
		"runnerctl_0.1.0_darwin_arm64.tar.gz",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CI_PROJECT_ID", "42")
	t.Setenv("CI_JOB_TOKEN", "test-job-token")
	t.Setenv("CI_COMMIT_SHA", releaseCommit)
	return root
}

func testReleaseServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("CI_API_V4_URL", srv.URL+"/api/v4")
	return srv
}

func TestPublishRejectsWrongOrInvalidTagBeforeUpload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"wrong commit", 200, fmt.Sprintf(`{"commit":{"id":%q}}`, otherCommit)},
		{"missing commit", 200, `{}`},
		{"invalid JSON", 200, `{`},
		{"lookup failure", 500, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testReleaseRoot(t)
			uploads := 0
			creates := 0
			srv := testReleaseServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/releases/v0.1.0":
					http.NotFound(w, r)
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/tags/v0.1.0":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				case r.Method == "PUT":
					uploads++
				case r.Method == "POST":
					creates++
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			})
			err := publishWithClient(srv.Client(), root)
			if err == nil || !strings.Contains(err.Error(), "tag") {
				t.Fatalf("expected tag error, got %v", err)
			}
			if uploads != 0 || creates != 0 {
				t.Fatalf("published after invalid tag: uploads=%d creates=%d", uploads, creates)
			}
		})
	}
}

func TestPublishVerifiesCreatedReleaseCommit(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"wrong commit", fmt.Sprintf(`{"commit":{"id":%q}}`, otherCommit)},
		{"missing commit", `{}`},
		{"invalid JSON", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testReleaseRoot(t)
			uploads := 0
			creates := 0
			srv := testReleaseServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/releases/v0.1.0":
					http.NotFound(w, r)
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/tags/v0.1.0":
					fmt.Fprintf(w, `{"commit":{"id":%q}}`, releaseCommit)
				case r.Method == "PUT":
					uploads++
					w.WriteHeader(http.StatusCreated)
				case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/releases":
					creates++
					w.WriteHeader(http.StatusCreated)
					fmt.Fprint(w, tc.body)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			})
			err := publishWithClient(srv.Client(), root)
			if err == nil || !strings.Contains(err.Error(), "created release") {
				t.Fatalf("expected created release mismatch, got %v", err)
			}
			if uploads != 5 || creates != 1 {
				t.Fatalf("unexpected publish calls: uploads=%d creates=%d", uploads, creates)
			}
		})
	}
}

func TestPublishWithMatchingOrMissingTag(t *testing.T) {
	for _, tagExists := range []bool{true, false} {
		name := "missing tag"
		if tagExists {
			name = "matching tag"
		}
		t.Run(name, func(t *testing.T) {
			root := testReleaseRoot(t)
			uploads := 0
			creates := 0
			srv := testReleaseServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/releases/v0.1.0":
					http.NotFound(w, r)
				case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/tags/v0.1.0":
					if tagExists {
						fmt.Fprintf(w, `{"commit":{"id":%q}}`, releaseCommit)
					} else {
						http.NotFound(w, r)
					}
				case r.Method == "PUT":
					uploads++
					w.WriteHeader(http.StatusCreated)
				case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/releases":
					creates++
					var payload struct {
						TagName string `json:"tag_name"`
						Ref     string `json:"ref"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.TagName != "v0.1.0" || payload.Ref != releaseCommit {
						t.Errorf("invalid release request: %+v, %v", payload, err)
					}
					w.WriteHeader(http.StatusCreated)
					fmt.Fprintf(w, `{"commit":{"id":%q}}`, releaseCommit)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			})
			if err := publishWithClient(srv.Client(), root); err != nil {
				t.Fatal(err)
			}
			if uploads != 5 || creates != 1 {
				t.Fatalf("unexpected publish calls: uploads=%d creates=%d", uploads, creates)
			}
		})
	}
}
