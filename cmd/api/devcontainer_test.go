package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The devcontainer rule, as tests.
//
// A devcontainer added once and never revisited drifts until the thing it
// exists to show is unreachable from it: a service listening on a port the
// container never forwards. The rule that prevents that - every service is
// forwarded and labeled - is only kept if something fails when it is not.

const devcontainerDir = "../../.devcontainer"

type devcontainer struct {
	Features map[string]any `json:"features"`
	// Either a port number or "service:port", which the spec allows and
	// Compose-based devcontainers use.
	ForwardPorts []any `json:"forwardPorts"`
	// Values are mostly strings but not only: requireLocalPort is a bool.
	PortsAttributes map[string]map[string]any `json:"portsAttributes"`
}

// readDevcontainer parses devcontainer.json.
//
// The file is JSONC. Rather than pull in a parser for one test, the file
// keeps to the subset the standard library can read once full-line comments
// are removed: no trailing commas and no comment after a value.
func readDevcontainer(t *testing.T) devcontainer {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(devcontainerDir, "devcontainer.json"))
	if err != nil {
		t.Fatalf("reading devcontainer.json: %v", err)
	}

	var kept []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}

	var config devcontainer
	if err := json.Unmarshal([]byte(strings.Join(kept, "\n")), &config); err != nil {
		// Two causes look identical from here: malformed JSON, or JSONC this
		// test does not read. Name both rather than guess.
		t.Fatalf("parsing devcontainer.json: %v\n"+
			"either it is malformed, or it uses JSONC this test does not read: "+
			"a trailing comma, or a comment after a value", err)
	}
	return config
}

// portKeys normalizes forwardPorts to the keys portsAttributes uses.
func portKeys(t *testing.T, ports []any) []string {
	t.Helper()
	keys := make([]string, 0, len(ports))
	for _, port := range ports {
		switch value := port.(type) {
		case float64:
			keys = append(keys, strconv.Itoa(int(value)))
		case string:
			keys = append(keys, value)
		default:
			t.Fatalf("forwardPorts entry %v is neither a number nor a string", port)
		}
	}
	return keys
}

// servicePort is the port the service listens on by default, read from the
// constant rather than written out again.
func servicePort(t *testing.T) string {
	t.Helper()
	_, port, err := net.SplitHostPort(defaultAddr)
	if err != nil {
		t.Fatalf("defaultAddr %q has no port: %v", defaultAddr, err)
	}
	return port
}

// Changing defaultAddr without changing the devcontainer fails here.
func TestTheServicePortIsForwarded(t *testing.T) {
	t.Parallel()

	want := servicePort(t)
	for _, key := range portKeys(t, readDevcontainer(t).ForwardPorts) {
		if key == want {
			return
		}
	}
	t.Errorf("port %s is what the service listens on but is not in forwardPorts", want)
}

// An unlabeled port shows up in the Ports panel as a bare number, and
// nobody demonstrating this to someone else knows which one to open.
func TestEveryForwardedPortIsLabelled(t *testing.T) {
	t.Parallel()

	config := readDevcontainer(t)
	keys := portKeys(t, config.ForwardPorts)
	if len(keys) == 0 {
		t.Fatal("forwardPorts is empty")
	}
	for _, key := range keys {
		label, _ := config.PortsAttributes[key]["label"].(string)
		if strings.TrimSpace(label) == "" {
			t.Errorf("port %s is forwarded but has no label in portsAttributes", key)
		}
	}
}

// A label for a port that is not forwarded is left over from a service that
// was removed, and it makes the Ports panel claim something is there.
func TestEveryLabelBelongsToAForwardedPort(t *testing.T) {
	t.Parallel()

	config := readDevcontainer(t)
	forwarded := map[string]bool{}
	for _, key := range portKeys(t, config.ForwardPorts) {
		forwarded[key] = true
	}
	for key := range config.PortsAttributes {
		if !forwarded[key] {
			t.Errorf("portsAttributes labels %s, which is not in forwardPorts", key)
		}
	}
}

// post-start.sh health-checks a port of its own, which is a second copy of
// the number. Changing defaultAddr and the JSON but not the script would
// leave the hook probing a port nothing listens on, forever and quietly.
func TestTheStartHookChecksTheServicePort(t *testing.T) {
	t.Parallel()

	script, err := os.ReadFile(filepath.Join(devcontainerDir, "post-start.sh"))
	if err != nil {
		t.Fatalf("reading post-start.sh: %v", err)
	}
	match := regexp.MustCompile(`(?m)^readonly port=(\d+)$`).FindSubmatch(script)
	if match == nil {
		t.Fatal("post-start.sh does not declare `readonly port=<n>`; this test cannot check it")
	}
	if got, want := string(match[1]), servicePort(t); got != want {
		t.Errorf("post-start.sh checks port %s, but the service listens on %s", got, want)
	}
}

// A hook without its execute bit fails with "permission denied" when the
// codespace is created, which is the one moment nobody is watching the log.
func TestTheLifecycleHooksAreExecutable(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"on-create.sh", "post-start.sh"} {
		info, err := os.Stat(filepath.Join(devcontainerDir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable (mode %s)", name, info.Mode().Perm())
		}
	}
	// Belt and braces: the JSON names these exact files.
	raw, err := os.ReadFile(filepath.Join(devcontainerDir, "devcontainer.json"))
	if err != nil {
		t.Fatalf("reading devcontainer.json: %v", err)
	}
	for _, name := range []string{"on-create.sh", "post-start.sh"} {
		if !strings.Contains(string(raw), fmt.Sprintf(".devcontainer/%s", name)) {
			t.Errorf("devcontainer.json does not reference .devcontainer/%s", name)
		}
	}
}

// Every feature is pinned in the lockfile. devcontainer.json names features by
// major version, which floats; the lockfile is what makes the build
// reproducible. A feature added without regenerating the lock floats for as
// long as nobody notices, which is the drift the lockfile exists to stop.
func TestEveryFeatureIsLocked(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join(devcontainerDir, "devcontainer-lock.json"))
	if err != nil {
		t.Fatalf("reading devcontainer-lock.json: %v\n"+
			"regenerate it with: npx @devcontainers/cli upgrade --workspace-folder .", err)
	}
	var lock struct {
		Features map[string]struct {
			Version  string `json:"version"`
			Resolved string `json:"resolved"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("parsing devcontainer-lock.json: %v", err)
	}

	for feature := range readDevcontainer(t).Features {
		entry, ok := lock.Features[feature]
		if !ok {
			t.Errorf("%s is not in devcontainer-lock.json; regenerate it with "+
				"npx @devcontainers/cli upgrade --workspace-folder .", feature)
			continue
		}
		if !strings.Contains(entry.Resolved, "@sha256:") {
			t.Errorf("%s is locked to %q, which is not a digest", feature, entry.Resolved)
		}
	}
	for feature := range lock.Features {
		if _, ok := readDevcontainer(t).Features[feature]; !ok {
			t.Errorf("devcontainer-lock.json pins %s, which devcontainer.json no longer uses", feature)
		}
	}
}
