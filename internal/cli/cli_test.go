package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const testOrg = "11111111-1111-4111-8111-111111111111"
const testProject = "22222222-2222-4222-8222-222222222222"
const testPool = "33333333-3333-4333-8333-333333333333"
const testToken = "rp_pat_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("RUNNERCTL_CONFIG_DIR", filepath.Join(t.TempDir(), "private-config"))
	t.Setenv("RUNNERCTL_TOKEN", "")
}
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func identityResponse() any {
	return map[string]any{"token_id": testPool, "organization": map[string]string{"id": testOrg, "name": "Test"}, "projects": []map[string]string{{"id": testProject, "name": "Demo"}}, "scopes": knownScopes}
}
func example() manifest {
	enabled := true
	m := manifest{APIVersion: "runner.retardsoftware.com/v1", Kind: "Pool", Context: "default", Spec: poolSpec{Name: "linux-ci", Environment: "ubuntu-latest", CPU: 2000, Memory: 8192, Disk: 41088, MaxJobMinutes: 60, MaxRunners: 3, Enabled: &enabled}}
	m.Metadata.ID = testPool
	m.Metadata.OrganizationID = testOrg
	m.Metadata.ProjectID = testProject
	return m
}
func manifestFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pool.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStrictManifestRejectsAmbiguityAndExecutableConfiguration(t *testing.T) {
	b, _ := yaml.Marshal(example())
	good := string(b)
	if _, err := readManifest(manifestFile(t, good)); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(manifestFile(t, strings.Replace(good, `site_id: ""`, "site_id: desktop-i9", 1))); err != nil {
		t.Fatal("valid named site rejected", err)
	}
	for name, text := range map[string]string{
		"duplicate":       good + "kind: Other\n",
		"unknown":         good + "token: some-secret\n",
		"documents":       good + "---\n" + good,
		"alias":           strings.Replace(good, "name: linux-ci", "name: &name linux-ci", 1),
		"merge":           strings.Replace(good, "spec:", "spec:\n    <<: {name: evil}", 1),
		"tag":             strings.Replace(good, "name: linux-ci", "name: !exec evil", 1),
		"missing_enabled": strings.Replace(good, "    enabled: true\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readManifest(manifestFile(t, text)); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
	// Text is data: the CLI never runs shells or expands commands from a manifest.
	literal := strings.Replace(good, "name: linux-ci", `name: '$(touch /tmp/should-not-exist)'`, 1)
	m, err := readManifest(manifestFile(t, literal))
	if err != nil || m.Spec.Name != "$(touch /tmp/should-not-exist)" {
		t.Fatal("literal configuration was not preserved", err)
	}
}

func TestPoolManifestVersions(t *testing.T) {
	for _, version := range []string{"runner.retardsoftware.com/v1", "runner.ser60v.com/v1", "runner.retardsoftware.com/v2", "runner.ser60v.com/v2", "other.example/v1", ""} {
		t.Run(version, func(t *testing.T) {
			m := example()
			m.APIVersion = version
			b, err := yaml.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readManifest(manifestFile(t, string(b)))
			supported := version == "runner.retardsoftware.com/v1" || version == "runner.ser60v.com/v1"
			if !supported {
				if err == nil {
					t.Fatal("unsupported manifest version accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Metadata != m.Metadata || got.Context != m.Context || !bytes.Equal(got.body(), m.body()) {
				t.Fatal("schema version changed the target or desired pool")
			}
			m.Kind = "Other"
			b, err = yaml.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readManifest(manifestFile(t, string(b))); err == nil {
				t.Fatal("unsupported manifest kind accepted")
			}
		})
	}
}

func TestInitEmitsCurrentPoolSchema(t *testing.T) {
	setup(t)
	path := filepath.Join(t.TempDir(), "pool.yaml")
	a := New(nil, io.Discard, io.Discard, "test")
	if code := a.Run(context.Background(), []string{"init", "--project", testProject, "-f", path}); code != 0 {
		t.Fatalf("init exited with %d", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("organization_id")) {
		t.Fatal("init emitted an organization field for the ordinary workflow")
	}
	m, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.APIVersion != "runner.retardsoftware.com/v1" || m.Metadata.OrganizationID != "" || m.Metadata.ProjectID != testProject {
		t.Fatal("init did not emit the current schema and requested scope")
	}
}

func TestSizeConversionIsExact(t *testing.T) {
	for input, want := range map[string]int{
		"40gb": 40960, "40.125gb": 41088, "40.125GiB": 41088,
		"8GB": 8192, "8192": 8192, "512mb": 512, "1tb": 1048576,
		"0.5gib": 512, "1.0009765625gb": 1025, " 40 GB ": 40960,
		"40,125 GB": 41088,
	} {
		got, err := parseSize(input)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "-1gb", "+1gb", "NaN", "1e3gb", "1.1mb", "0.0001gb", "1.5", "1kb", "1,234,567gb", "1,5.0gb", "1 G B", "999999999999999999999999tb"} {
		if _, err := parseSize(input); err == nil {
			t.Errorf("parseSize(%q) accepted invalid size", input)
		}
	}
	for size, want := range map[int]string{512: "512mb", 8192: "8gb", 41088: "40.125gb", 1025: "1025mb", 1048576: "1tb"} {
		if got := formatSize(size); got != want {
			t.Errorf("formatSize(%d) = %q; want %q", size, got, want)
		}
	}
}

func TestManifestSizeAliasesAndStrictness(t *testing.T) {
	b, err := yaml.Marshal(example())
	if err != nil {
		t.Fatal(err)
	}
	good := string(b)
	if !strings.Contains(good, "memory: 8gb\n") || !strings.Contains(good, "disk: 40.125gb\n") {
		t.Fatalf("YAML does not contain readable exact sizes: %s", good)
	}
	for _, replacement := range []string{"memory_mib: 8192", "memory_mib: 8gb", "memory_mib: '8GiB'", "memory: 8192"} {
		text := strings.Replace(good, "memory: 8gb", replacement, 1)
		got, err := readManifest(manifestFile(t, text))
		if err != nil || got.Spec.Memory != 8192 {
			t.Errorf("compatible memory %q: %v", replacement, err)
		}
	}
	for _, replacement := range []string{"disk_mib: 41088", "disk_mib: 40.125gb", "disk: 41088"} {
		text := strings.Replace(good, "disk: 40.125gb", replacement, 1)
		got, err := readManifest(manifestFile(t, text))
		if err != nil || got.Spec.Disk != 41088 {
			t.Errorf("compatible disk %q: %v", replacement, err)
		}
	}
	for _, text := range []string{
		strings.Replace(good, "memory: 8gb", "memory: 8gb\n    memory_mib: 8192", 1),
		strings.Replace(good, "disk: 40.125gb", "disk: 40.125gb\n    disk_mib: 41088", 1),
		strings.Replace(good, "memory: 8gb", "memory: 8gb\n    memory: 8gb", 1),
		strings.Replace(good, "memory: 8gb", "memry: 8gb", 1),
		strings.Replace(good, "memory: 8gb", "memory: 1.1mb", 1),
	} {
		if _, err := readManifest(manifestFile(t, text)); err == nil {
			t.Errorf("accepted ambiguous or invalid YAML:\n%s", text)
		}
	}
}

func TestInitSizeFlagsPreserveRequestValues(t *testing.T) {
	setup(t)
	for _, tc := range []struct {
		memory, disk         string
		wantMemory, wantDisk int
	}{{"8GB", "40gb", 8192, 40960}, {"8192", "41088", 8192, 41088}, {"0.5gib", "40.125gb", 512, 41088}, {" 8 GB ", "40,125 GB", 8192, 41088}} {
		path := filepath.Join(t.TempDir(), "pool.yaml")
		var stderr bytes.Buffer
		a := New(nil, io.Discard, &stderr, "test")
		if code := a.Run(context.Background(), []string{"init", "--organization", testOrg, "--project", testProject, "--memory", tc.memory, "--disk", tc.disk, "-f", path}); code != 0 {
			t.Fatalf("init returned %d: %s", code, stderr.String())
		}
		m, err := readManifest(path)
		if err != nil || m.Spec.Memory != tc.wantMemory || m.Spec.Disk != tc.wantDisk {
			t.Fatalf("init/read values = %d/%d, %v; want %d/%d", m.Spec.Memory, m.Spec.Disk, err, tc.wantMemory, tc.wantDisk)
		}
		var body map[string]any
		if err := json.Unmarshal(m.body(), &body); err != nil {
			t.Fatal(err)
		}
		if body["memory_mib"] != float64(tc.wantMemory) || body["disk_mib"] != float64(tc.wantDisk) {
			t.Fatalf("API body changed units: %v", body)
		}
	}
	for _, bad := range []string{"1.1mb", "NaN", "-1gb", "1e3gb"} {
		path := filepath.Join(t.TempDir(), "pool.yaml")
		a := New(nil, io.Discard, io.Discard, "test")
		if code := a.Run(context.Background(), []string{"init", "--organization", testOrg, "--project", testProject, "--memory", bad, "-f", path}); code != 2 {
			t.Errorf("init accepted %q: code %d", bad, code)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("init wrote a file for %q", bad)
		}
	}
}

func TestCredentialsPrivateAndOriginBound(t *testing.T) {
	setup(t)
	if err := saveCredential("default", credential{"https://one.example", testToken}); err != nil {
		t.Fatal(err)
	}
	a := New(nil, io.Discard, io.Discard, "test")
	a.server = "https://two.example"
	if _, err := a.client(); err == nil {
		t.Fatal("token sent to a different origin")
	}
	for _, raw := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/path", "https://example.com?token=x", "https://example.com/#fragment"} {
		if _, err := canonicalServer(raw); err == nil {
			t.Fatalf("accepted unsafe origin %s", raw)
		}
	}
}

func TestNoRedirectCredentialLeakAndErrorRedaction(t *testing.T) {
	calls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; reply(w, map[string]bool{"ok": true}) }))
	defer foreign.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, err := newClient(srv.URL, testToken).json(context.Background(), "GET", "/api/app/v1/pools", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 0 {
		t.Fatal("redirect followed")
	}
	leak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		reply(w, map[string]any{"error": map[string]string{"code": "bad", "message": testToken + " synthetic-password"}})
	}))
	defer leak.Close()
	c := newClient(leak.URL, testToken)
	c.secrets = []string{"synthetic-password"}
	_, err := c.json(context.Background(), "GET", "/api/app/v1/pools", nil)
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "synthetic-password") {
		t.Fatal("secret leaked", err)
	}
}

func TestApplyUsesOptimisticPreconditionsAndDoesNotRepeatNoop(t *testing.T) {
	for _, action := range []string{"create", "update", "none", "conflict"} {
		t.Run(action, func(t *testing.T) {
			setup(t)
			writes := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Error("missing token")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/cli/context"):
					reply(w, identityResponse())
				case strings.HasSuffix(r.URL.Path, "/plan"):
					revision := int64(4)
					planAction := action
					if action == "create" {
						revision = 0
					}
					if action == "conflict" {
						planAction = "update"
					}
					reply(w, map[string]any{"pool_id": testPool, "revision": revision, "action": planAction, "errors": []any{}})
				case r.Method == "PUT":
					writes++
					if action == "create" {
						if r.Header.Get("If-None-Match") != "*" {
							t.Error("missing create precondition")
						}
					} else if r.Header.Get("If-Match") != `"4"` {
						t.Error("missing revision precondition")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["project_id"] != testProject || body["max_runners"] != float64(3) {
						t.Error("wrong desired pool", body)
					}
					if action == "conflict" {
						w.WriteHeader(412)
						reply(w, map[string]any{"error": map[string]string{"code": "precondition_failed", "message": "changed"}})
					} else {
						reply(w, map[string]any{"id": testPool, "revision": 5})
					}
				default:
					t.Error("unexpected request", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			if err := saveCredential("default", credential{srv.URL, testToken}); err != nil {
				t.Fatal(err)
			}
			data, _ := yaml.Marshal(example())
			path := manifestFile(t, string(data))
			var out, errout bytes.Buffer
			a := New(nil, &out, &errout, "test")
			exit := a.Run(context.Background(), []string{"--json", "apply", "-f", path})
			want := 0
			if action == "conflict" {
				want = 3
			}
			if exit != want {
				t.Fatalf("exit=%d, %s", exit, errout.String())
			}
			wantWrites := 1
			if action == "none" {
				wantWrites = 0
			}
			if writes != wantWrites {
				t.Fatal("unexpected mutation count", writes)
			}
			if strings.Contains(out.String()+errout.String(), testToken) {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestManifestCannotChangeOrganization(t *testing.T) {
	setup(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; reply(w, identityResponse()) }))
	defer srv.Close()
	_ = saveCredential("default", credential{srv.URL, testToken})
	m := example()
	m.Metadata.OrganizationID = testPool
	b, _ := yaml.Marshal(m)
	path := manifestFile(t, string(b))
	for _, command := range []string{"plan", "apply"} {
		a := New(nil, io.Discard, io.Discard, "test")
		if exit := a.Run(context.Background(), []string{command, "-f", path}); exit == 0 {
			t.Fatal("foreign org accepted by", command)
		}
	}
	if calls != 2 {
		t.Fatal("planned or wrote before organization verification")
	}
}

func TestManifestWithoutOrganizationUsesTokenProject(t *testing.T) {
	for _, command := range []string{"plan", "apply"} {
		t.Run(command, func(t *testing.T) {
			setup(t)
			identityCalls, planCalls, writes := 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/app/v1/cli/context":
					identityCalls++
					reply(w, identityResponse())
				case r.Method == "POST" && r.URL.Path == "/api/app/v1/pools/"+testPool+"/plan":
					planCalls++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["project_id"] != testProject || body["memory_mib"] != float64(8192) || body["disk_mib"] != float64(41088) {
						t.Error("wrong plan body", body)
					}
					reply(w, map[string]any{"pool_id": testPool, "revision": 0, "action": "create", "errors": []any{}})
				case r.Method == "PUT" && r.URL.Path == "/api/app/v1/pools/"+testPool:
					writes++
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("missing create precondition")
					}
					reply(w, map[string]any{"id": testPool, "revision": 1})
				default:
					t.Error("unexpected request", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			if err := saveCredential("default", credential{srv.URL, testToken}); err != nil {
				t.Fatal(err)
			}
			m := example()
			m.Metadata.OrganizationID = ""
			b, err := yaml.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte("organization_id")) {
				t.Fatal("empty organization field was emitted")
			}
			var stderr bytes.Buffer
			a := New(nil, io.Discard, &stderr, "test")
			if exit := a.Run(context.Background(), []string{command, "-f", manifestFile(t, string(b))}); exit != 0 {
				t.Fatalf("%s failed: exit %d: %s", command, exit, stderr.String())
			}
			wantWrites := 0
			if command == "apply" {
				wantWrites = 1
			}
			if identityCalls != 1 || planCalls != 1 || writes != wantWrites {
				t.Fatalf("identity=%d plan=%d writes=%d", identityCalls, planCalls, writes)
			}
		})
	}
}

func TestManifestWithoutOrganizationRejectsInaccessibleProject(t *testing.T) {
	setup(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/app/v1/cli/context" {
			t.Error("request after inaccessible project", r.Method, r.URL.Path)
		}
		identity := identityResponse().(map[string]any)
		identity["projects"] = []any{}
		reply(w, identity)
	}))
	defer srv.Close()
	if err := saveCredential("default", credential{srv.URL, testToken}); err != nil {
		t.Fatal(err)
	}
	m := example()
	m.Metadata.OrganizationID = ""
	b, _ := yaml.Marshal(m)
	path := manifestFile(t, string(b))
	for _, command := range []string{"plan", "apply"} {
		a := New(nil, io.Discard, io.Discard, "test")
		if exit := a.Run(context.Background(), []string{command, "-f", path}); exit == 0 {
			t.Fatal("inaccessible project accepted by", command)
		}
	}
	if calls != 2 {
		t.Fatal("expected only identity requests, got", calls)
	}
}

func TestMalformedExplicitOrganizationIsRejected(t *testing.T) {
	m := example()
	m.Metadata.OrganizationID = "not-a-uuid"
	b, _ := yaml.Marshal(m)
	if _, err := readManifest(manifestFile(t, string(b))); err == nil {
		t.Fatal("malformed explicit organization accepted")
	}
	m.Metadata.OrganizationID = ""
	b, _ = yaml.Marshal(m)
	for _, value := range []string{`""`, "null"} {
		text := strings.Replace(string(b), "  project_id: "+testProject, "  organization_id: "+value+"\n  project_id: "+testProject, 1)
		if _, err := readManifest(manifestFile(t, text)); err == nil {
			t.Errorf("explicit %s organization accepted", value)
		}
	}
	setup(t)
	path := filepath.Join(t.TempDir(), "pool.yaml")
	a := New(nil, io.Discard, io.Discard, "test")
	if exit := a.Run(context.Background(), []string{"init", "--project", testProject, "--organization", "not-a-uuid", "-f", path}); exit != 2 {
		t.Fatal("malformed init organization accepted", exit)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("init wrote a manifest for malformed organization")
	}
	path = filepath.Join(t.TempDir(), "empty-org-pool.yaml")
	if exit := a.Run(context.Background(), []string{"init", "--project", testProject, "--organization", "", "-f", path}); exit != 2 {
		t.Fatal("explicit empty init organization accepted", exit)
	}
}

func TestManifestCannotSwitchContextOrRedirectEnvironmentToken(t *testing.T) {
	setup(t)
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++; reply(w, identityResponse()) }))
	defer foreign.Close()
	if err := saveCredential("other", credential{foreign.URL, testToken}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNERCTL_TOKEN", testToken)
	a := New(nil, io.Discard, io.Discard, "test")
	a.context = "other"
	c, err := a.client()
	if err != nil {
		t.Fatal(err)
	}
	if c.server != defaultServer {
		t.Fatal("environment token inherited origin from stored context")
	}
	m := example()
	m.Context = "other"
	b, _ := yaml.Marshal(m)
	path := manifestFile(t, string(b))
	for _, args := range [][]string{{"apply", "-f", path}, {"--context", "default", "apply", "-f", path}} {
		a = New(nil, io.Discard, io.Discard, "test")
		if exit := a.Run(context.Background(), args); exit != 2 {
			t.Fatal("manifest switched selected context", exit)
		}
	}
	if foreignCalls != 0 {
		t.Fatal("token was sent before context verification")
	}
}

func TestWatchTerminalCancelledNDJSONAndBoundedTimeout(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, "{\n\"status\": \"cancelled\", \"duration_seconds\": 560\n}")
	}))
	defer srv.Close()
	var out bytes.Buffer
	a := New(nil, &out, io.Discard, "test")
	a.json = true
	err := a.watch(context.Background(), newClient(srv.URL, testToken), testPool, time.Second)
	ce, ok := err.(*commandError)
	if !ok || ce.exit != 4 || calls != 1 {
		t.Fatal("cancelled did not terminate", err, calls)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatal("not NDJSON")
	}
	wait := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
		reply(w, map[string]any{"error": map[string]string{"code": "rate_limited"}})
	}))
	defer wait.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := a.watch(ctx, newClient(wait.URL, testToken), testPool, time.Second); err != context.DeadlineExceeded {
		t.Fatal("timeout lost", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("retry ignored context")
	}
	for seconds, want := range map[float64]string{0: "0s", 59: "59s", 60: "1m 0s", 560: "9m 20s", 3661: "1h 1m 1s"} {
		if got := duration(seconds); got != want {
			t.Errorf("%v: %s", seconds, got)
		}
	}
}

func TestInteractiveLoginUsesCSRFBoundProofAndOnlyPersistsPAT(t *testing.T) {
	for _, twoFA := range []bool{false, true} {
		t.Run(fmt.Sprint(twoFA), func(t *testing.T) {
			setup(t)
			var boundHash string
			logout := false
			confirmationCalls := 0
			loginVerified := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" && r.Header.Get("X-CSRF-Token") != "csrf-secret" {
					t.Error("CSRF missing")
				}
				switch r.URL.Path {
				case "/api/auth/v1/auth/csrf":
					http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "csrf-secret", Path: "/"})
					reply(w, map[string]string{"csrf_token": "csrf-secret"})
				case "/api/auth/v1/auth/login":
					if twoFA {
						http.SetCookie(w, &http.Cookie{Name: "nonce", Value: "login-nonce", Path: "/"})
						w.WriteHeader(202)
						reply(w, map[string]string{"challenge_id": "login-challenge"})
					} else {
						http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-secret", Path: "/"})
						reply(w, map[string]bool{"ok": true})
					}
				case "/api/auth/v1/auth/login/verify":
					if _, err := r.Cookie("nonce"); err != nil {
						t.Error("missing nonce")
					}
					loginVerified = true
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-secret", Path: "/"})
					reply(w, map[string]bool{"ok": true})
				case "/api/app/v1/projects":
					reply(w, map[string]any{"items": []map[string]string{{"id": testProject}}})
				case "/api/auth/v1/me/security":
					reply(w, map[string]bool{"email_two_factor_enabled": twoFA})
				case "/api/auth/v1/auth/reauthenticate":
					var proof map[string]string
					_ = json.NewDecoder(r.Body).Decode(&proof)
					boundHash = proof["body_sha256"]
					if proof["path"] != "/api/auth/v1/me/personal-tokens" || proof["organization_id"] != testOrg || proof["method"] != "POST" {
						t.Error("unbound confirmation", proof)
					}
					confirmationCalls++
					if twoFA && confirmationCalls == 1 {
						if proof["password"] != "" {
							t.Error("password used instead of 2FA")
						}
						w.WriteHeader(202)
						reply(w, map[string]string{"challenge_id": "confirmation"})
					} else {
						if twoFA && proof["code"] != "123456" {
							t.Error("confirmation code absent")
						}
						reply(w, map[string]string{"token": "proof-secret"})
					}
				case "/api/auth/v1/me/personal-tokens":
					b, _ := io.ReadAll(r.Body)
					sum := sha256.Sum256(b)
					if hex.EncodeToString(sum[:]) != boundHash || r.Header.Get("X-Operation-Token") != "proof-secret" {
						t.Error("body/proof mismatch")
					}
					if _, err := r.Cookie("session"); err != nil {
						t.Error("missing session")
					}
					w.WriteHeader(201)
					reply(w, map[string]any{"token": testToken, "personal_token": map[string]string{"id": testPool}})
				case "/api/app/v1/cli/context":
					if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer "+testToken {
						t.Error("PAT mixed with cookie")
					}
					reply(w, identityResponse())
				case "/api/auth/v1/auth/logout":
					logout = true
					w.WriteHeader(204)
				default:
					t.Error("unexpected route", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			var out, errout bytes.Buffer
			a := New(nil, &out, &errout, "test")
			a.prompt = func(label string, secret bool) (string, error) {
				if strings.Contains(label, "Password") {
					return "password-secret", nil
				}
				if strings.Contains(label, "Project numbers") {
					return "1", nil
				}
				return "123456", nil
			}
			args := []string{"--server", srv.URL, "--json", "auth", "login", "--email", "test@example.test", "--organization", testOrg}
			if !twoFA {
				args = append(args, "--projects", testProject)
			}
			exit := a.Run(context.Background(), args)
			if exit != 0 {
				t.Fatal(exit, errout.String())
			}
			if !logout || twoFA && !loginVerified {
				t.Fatal("session lifecycle incomplete")
			}
			path, _ := credentialPath("default")
			stored, _ := os.ReadFile(path)
			for _, secret := range []string{"session-secret", "password-secret", "proof-secret", "csrf-secret", "123456"} {
				if bytes.Contains(stored, []byte(secret)) {
					t.Fatal("bootstrap secret persisted")
				}
			}
			loaded, err := loadCredential("default")
			if err != nil || loaded.Token != testToken {
				t.Fatal("PAT missing")
			}
			for _, secret := range []string{testToken, "password-secret", "proof-secret", "csrf-secret"} {
				if strings.Contains(out.String()+errout.String(), secret) {
					t.Fatal("secret printed")
				}
			}
		})
	}
}
