package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunnerListFiltersAndInputValidation(t *testing.T) {
	setup(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api/app/v1/runners" || r.Header.Get("Authorization") != "Bearer "+testToken || q.Get("project_id") != testProject || q.Get("pool_id") != testPool || q.Get("active") != "false" || q.Get("status") != "failed" || q.Get("limit") != "20" || q.Get("offset") != "2" {
			t.Errorf("incorrect runner list request: %s", r.URL)
		}
		reply(w, map[string]any{"items": []any{}, "total": 0, "limit": 20, "offset": 2})
	}))
	defer srv.Close()
	if err := saveCredential("default", credential{srv.URL, testToken}); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	a := New(nil, &out, &stderr, "test")
	if code := a.Run(context.Background(), []string{"--json", "runners", "list", "--all", "--project", testProject, "--pool", testPool, "--status", "failed", "--limit", "20", "--offset", "2"}); code != 0 || !json.Valid(out.Bytes()) {
		t.Fatalf("list failed: %d %s", code, stderr.String())
	}
	for _, args := range [][]string{
		{"list", "--pool", "../foreign"}, {"list", "--status", "unexpected"},
		{"list", "--limit", "101"}, {"get", "../foreign"},
		{"watch", testPool, "--interval", "0s"}, {"cancel", testPool},
	} {
		a := New(nil, io.Discard, io.Discard, "test")
		if code := a.Run(context.Background(), append([]string{"runners"}, args...)); code != 2 {
			t.Errorf("invalid %v returned %d", args, code)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid input sent requests: %d", calls)
	}
}

func TestRunnerWatchWaitsForAuthoritativeCleanup(t *testing.T) {
	steps := []map[string]any{
		{"status": "succeeded", "observation_source": "api_projection", "manager_status": "unavailable", "phase": nil},
		{"status": "succeeded", "observation_source": "manager", "manager_status": "available", "phase": "deleting"},
		{"status": "succeeded", "observation_source": "manager", "manager_status": "available", "phase": "accounting"},
		{"status": "succeeded", "observation_source": "manager", "manager_status": "available", "phase": "done"},
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/app/v1/runners/"+testPool {
			t.Error("watch attempted a control action or wrong path")
		}
		if calls >= len(steps) {
			t.Error("watch did not stop after done")
			w.WriteHeader(500)
			return
		}
		v := steps[calls]
		v["id"] = testPool
		calls++
		reply(w, v)
	}))
	defer srv.Close()
	var out bytes.Buffer
	a := New(nil, &out, io.Discard, "test")
	a.json = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.observe(ctx, newClient(srv.URL, testToken), "/api/app/v1/runners/"+testPool, testPool, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || calls != 4 {
		t.Fatalf("watch accepted premature completion: %d calls, %d observations", calls, len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal("invalid NDJSON")
		}
	}
}

func TestRunnerWatchFailureUnavailableAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, id, source, manager, phase, status string
		wantExit                                 int
	}{
		{"failed", testPool, "manager", "available", "done", "failed", 4},
		{"unavailable", testPool, "api_projection", "unavailable", "done", "succeeded", 5},
		{"foreign", testProject, "manager", "available", "done", "succeeded", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reply(w, map[string]any{"id": tc.id, "status": tc.status, "phase": tc.phase, "manager_status": tc.manager, "observation_source": tc.source})
			}))
			defer srv.Close()
			if err := saveCredential("default", credential{srv.URL, testToken}); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			a := New(nil, &out, &stderr, "test")
			if code := a.Run(context.Background(), []string{"--json", "runners", "watch", testPool, "--timeout", "50ms"}); code != tc.wantExit || !json.Valid(stderr.Bytes()) {
				t.Fatalf("exit %d wanted %d: %s", code, tc.wantExit, stderr.String())
			}
			if tc.name == "foreign" && out.Len() != 0 {
				t.Fatal("wrong identity emitted")
			}
		})
	}
}

func TestPublicStatusWatchNeverSendsCredentialsAndIsFinite(t *testing.T) {
	setup(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" || r.URL.Path != "/api/app/v1/status" || r.Method != "GET" {
			t.Error("public status received credentials or wrong request")
		}
		reply(w, map[string]any{"status": "operational", "stale": false})
	}))
	defer srv.Close()
	if err := saveCredential("default", credential{srv.URL, testToken}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNERCTL_TOKEN", testToken)
	var out, stderr bytes.Buffer
	a := New(nil, &out, &stderr, "test")
	if code := a.Run(context.Background(), []string{"--json", "status", "watch", "--timeout", "50ms"}); code != 5 || !json.Valid(out.Bytes()) || !json.Valid(stderr.Bytes()) || calls != 1 {
		t.Fatalf("status watch: %d calls=%d out=%s error=%s", code, calls, out.String(), stderr.String())
	}
}

func TestObservationHonorsRetryAfterAndCancellation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		reply(w, map[string]any{"error": map[string]string{"code": "rate_limited"}})
	}))
	defer srv.Close()
	var out, stderr bytes.Buffer
	a := New(nil, &out, &stderr, "test")
	a.json = true
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := a.observe(ctx, newClient(srv.URL, ""), "/api/app/v1/status", "", time.Millisecond); !errors.Is(err, context.DeadlineExceeded) || calls != 1 || out.Len() != 0 || !json.Valid(stderr.Bytes()) {
		t.Fatalf("retry ignored bound: %v calls=%d stderr=%s", err, calls, stderr.String())
	}
	ctx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := a.observe(ctx, newClient(srv.URL, ""), "/api/app/v1/status", "", time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel was ignored", err)
	}
}

func TestMachineHelpIsOfflineAndDiscoverable(t *testing.T) {
	setup(t)
	// These offline commands must not attempt to parse or use credentials.
	t.Setenv("RUNNERCTL_TOKEN", "deliberately-invalid")
	for _, args := range [][]string{{"--json", "help"}, {"schema"}, {"--json"}} {
		var out, stderr bytes.Buffer
		a := New(nil, &out, &stderr, "0.1.2")
		if code := a.Run(context.Background(), args); code != 0 {
			t.Fatalf("offline help: %d %s", code, stderr.String())
		}
		var contract struct {
			Version  int           `json:"schema_version"`
			Commands []commandSpec `json:"commands"`
		}
		if err := json.Unmarshal(out.Bytes(), &contract); err != nil || contract.Version != 1 {
			t.Fatal("invalid command contract", err)
		}
		found := map[string]bool{}
		for _, c := range contract.Commands {
			found[c.Command] = true
			if strings.HasSuffix(c.Command, "watch") && c.Mutates {
				t.Fatal("watch is a mutation")
			}
		}
		for _, required := range []string{"runners list", "runners get", "runners watch", "status watch", "apply", "auth login"} {
			if !found[required] {
				t.Error("missing command", required)
			}
		}
	}
}
