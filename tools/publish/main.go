// publish is the CI-only release publisher. Credentials are read from the job
// environment and never printed or placed in arguments, URLs or artifacts.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func main() {
	if err := publish(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func publish() error {
	base := os.Getenv("CI_API_V4_URL")
	project := os.Getenv("CI_PROJECT_ID")
	token := os.Getenv("CI_JOB_TOKEN")
	commit := os.Getenv("CI_COMMIT_SHA")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !regexp.MustCompile(`^[0-9]+$`).MatchString(project) || token == "" || !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(commit) {
		return fmt.Errorf("invalid release environment")
	}
	b, err := os.ReadFile("VERSION")
	if err != nil {
		return err
	}
	version := strings.TrimSpace(string(b))
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		return fmt.Errorf("invalid VERSION")
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, contentType string, body io.Reader) (int, []byte, error) {
		req, err := http.NewRequest(method, base+"/projects/"+project+path, body)
		if err != nil {
			return 0, nil, fmt.Errorf("cannot construct release request")
		}
		req.Header.Set("JOB-TOKEN", token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("release transport failed")
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
		if err != nil || len(data) > 1024*1024 {
			return 0, nil, fmt.Errorf("invalid release response")
		}
		return res.StatusCode, data, nil
	}
	status, data, err := request("GET", "/releases/v"+version, "", nil)
	if err != nil {
		return err
	}
	if status == 200 {
		var existing struct {
			Commit struct{ ID string } `json:"commit"`
		}
		if json.Unmarshal(data, &existing) != nil || existing.Commit.ID != commit {
			return fmt.Errorf("release version already belongs to another commit")
		}
		fmt.Println("Release already published for this commit")
		return nil
	}
	if status != 404 {
		return fmt.Errorf("release lookup returned HTTP %d", status)
	}
	files, err := filepath.Glob(".build/release/*")
	if err != nil {
		return err
	}
	if len(files) != 5 {
		return fmt.Errorf("expected four archives and SHA256SUMS")
	}
	links := []map[string]string{}
	for _, path := range files {
		name := filepath.Base(path)
		if name != "SHA256SUMS" && !regexp.MustCompile(`^runnerctl_`+regexp.QuoteMeta(version)+`_(linux|darwin)_(amd64|arm64)\.tar\.gz$`).MatchString(name) {
			return fmt.Errorf("unexpected release asset")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		endpoint := "/packages/generic/runnerctl/" + version + "/" + name
		status, _, err := request("PUT", endpoint, "application/octet-stream", f)
		f.Close()
		if err != nil {
			return err
		}
		if status != 201 {
			return fmt.Errorf("asset upload returned HTTP %d", status)
		}
		links = append(links, map[string]string{"name": name, "url": base + "/projects/" + project + endpoint, "direct_asset_path": "/" + name, "link_type": "package"})
	}
	body, _ := json.Marshal(map[string]any{"name": "runnerctl " + version, "tag_name": "v" + version, "ref": commit, "description": "Runner Platform CLI. Verify downloads against SHA256SUMS. See README for authentication and declarative pool management.", "assets": map[string]any{"links": links}})
	status, _, err = request("POST", "/releases", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("release creation returned HTTP %d", status)
	}
	fmt.Println("Published runnerctl", version)
	return nil
}
