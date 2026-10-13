package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// The Dockerfile and compose.yaml, held to the code.
//
// Each test here compares a value written in a container file against the
// value the code actually uses. They are two copies of one fact, and the
// copies drift: the Go version bumped in go.mod but not the image, the drain
// lengthened but not the grace Compose allows it.

// Relative to this package, where go test runs.
const (
	repoRoot       = "../.."
	dockerDir      = repoRoot + "/infra/docker"
	composePath    = dockerDir + "/compose.yaml"
	dockerfilePath = dockerDir + "/Dockerfile"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	// Short syntax ("8081:8080") or long syntax (a mapping); both are legal.
	Build           composeBuild `yaml:"build"`
	Ports           []any        `yaml:"ports"`
	StopGracePeriod string       `yaml:"stop_grace_period"`
	ReadOnly        bool         `yaml:"read_only"`
	CapDrop         []string     `yaml:"cap_drop"`
}

type composeBuild struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

func readCompose(t *testing.T) composeFile {
	t.Helper()
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("reading compose.yaml: %v", err)
	}
	var file composeFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing compose.yaml: %v", err)
	}
	return file
}

func readDockerfile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	return string(raw)
}

// publishedPort returns the host side of a Compose port mapping, or "" when
// the mapping publishes nothing to the host.
func publishedPort(t *testing.T, entry any) string {
	t.Helper()
	switch value := entry.(type) {
	case string:
		mapping, _, _ := strings.Cut(value, "/") // drop a trailing /tcp or /udp
		parts := strings.Split(mapping, ":")
		switch len(parts) {
		case 1:
			return "" // container port only; the host side is ephemeral
		case 2:
			return parts[0]
		default:
			return parts[len(parts)-2] // "ip:host:container"
		}
	case int:
		return ""
	case map[string]any:
		if published, ok := value["published"]; ok {
			return fmt.Sprint(published)
		}
		return ""
	default:
		t.Fatalf("a port entry of type %T is neither short nor long syntax", entry)
		return ""
	}
}

// Compose builds from the repository root with the Dockerfile the tests here
// read, and the ignore file sits where the builder looks for it: beside the
// Dockerfile, named after it. Renaming or moving one without the others
// leaves the image built from a different file than the one tested, or sends
// .git and .tools into the build context.
func TestComposeBuildsTheDockerfileTheseTestsRead(t *testing.T) {
	t.Parallel()

	api, ok := readCompose(t).Services["api"]
	if !ok {
		t.Fatal("compose.yaml has no api service")
	}
	if api.Build.Context == "" || api.Build.Dockerfile == "" {
		t.Fatalf("the api build needs both context and dockerfile, got %+v", api.Build)
	}

	buildRoot := mustAbs(t, filepath.Join(dockerDir, api.Build.Context))
	if root := mustAbs(t, repoRoot); buildRoot != root {
		t.Errorf("the build context is %s, want the repository root %s", buildRoot, root)
	}
	if got, want := filepath.Join(buildRoot, api.Build.Dockerfile), mustAbs(t, dockerfilePath); got != want {
		t.Errorf("Compose builds %s, but these tests read %s", got, want)
	}

	if _, err := os.Stat(repoRoot + "/.dockerignore"); err == nil {
		t.Error("a .dockerignore at the repository root is shadowed by Dockerfile.dockerignore, " +
			"so its rules would look active and apply to nothing")
	}
}

// The build stage runs COPY . ., so whatever the ignore file lets through
// lands in a layer and the build cache. Repository history, a hundred
// megabytes of tools, and local credentials stay out.
func TestTheBuildContextLeavesOutHistoryToolsAndSecrets(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(dockerfilePath + ".dockerignore")
	if err != nil {
		t.Fatalf("no ignore file beside the Dockerfile: %v", err)
	}
	patterns := map[string]bool{}
	for line := range strings.Lines(string(raw)) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			patterns[line] = true
		}
	}
	for _, want := range []string{".git", ".tools", "bin", ".env*", "*.pem", "*.key"} {
		if !patterns[want] {
			t.Errorf("Dockerfile.dockerignore does not exclude %s", want)
		}
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	return abs
}

// The image builds with the Go version go.mod names. GOTOOLCHAIN=local makes
// the build fail on a mismatch anyway; this fails first, with a clearer
// message, and without needing Docker.
func TestTheBuildImageUsesTheGoVersionInGoMod(t *testing.T) {
	t.Parallel()

	goMod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	want := regexp.MustCompile(`(?m)^go (\d+\.\d+\.\d+)$`).FindSubmatch(goMod)
	if want == nil {
		t.Fatal("go.mod does not name a full Go version (go x.y.z)")
	}
	got := regexp.MustCompile(`golang:(\d+\.\d+\.\d+)-`).FindStringSubmatch(readDockerfile(t))
	if got == nil {
		t.Fatal("the Dockerfile does not build from a golang:x.y.z image")
	}
	if got[1] != string(want[1]) {
		t.Errorf("the Dockerfile builds with Go %s, but go.mod requires %s", got[1], want[1])
	}
}

// Compose must wait longer than the service drains. Its default is ten
// seconds; the drain is fifteen. Shorter, and Compose kills the process
// partway through finishing the requests graceful shutdown exists to finish.
func TestComposeAllowsTheServiceToFinishDraining(t *testing.T) {
	t.Parallel()

	api, ok := readCompose(t).Services["api"]
	if !ok {
		t.Fatal("compose.yaml has no api service")
	}
	if api.StopGracePeriod == "" {
		t.Fatalf("stop_grace_period is unset, so Compose uses its default of 10s against a %s drain",
			defaultGrace)
	}
	grace, err := time.ParseDuration(api.StopGracePeriod)
	if err != nil {
		t.Fatalf("stop_grace_period %q: %v", api.StopGracePeriod, err)
	}
	if grace <= defaultGrace {
		t.Errorf("stop_grace_period is %s but the service drains for up to %s; "+
			"Compose would SIGKILL it first", grace, defaultGrace)
	}
}

// Every port Compose publishes is forwarded by the devcontainer. This is the
// same rule as the service's own port, extended to everything Compose runs,
// so a store added later without a forwarded port fails here.
func TestEveryComposePortIsForwardedByTheDevcontainer(t *testing.T) {
	t.Parallel()

	forwarded := map[string]bool{}
	for _, key := range portKeys(t, readDevcontainer(t).ForwardPorts) {
		forwarded[key] = true
	}
	for name, service := range readCompose(t).Services {
		for _, entry := range service.Ports {
			port := publishedPort(t, entry)
			if port == "" {
				continue
			}
			if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
				t.Errorf("service %s publishes %q, which is not a port", name, port)
				continue
			}
			if !forwarded[port] {
				t.Errorf("service %s publishes port %s, which the devcontainer does not forward", name, port)
			}
		}
	}
}

// The image runs as a numbered, unprivileged user. Kubernetes can only check
// runAsNonRoot against a number, so a name would pass review and fail the
// pod.
func TestTheImageRunsAsANumberedNonRootUser(t *testing.T) {
	t.Parallel()

	users := regexp.MustCompile(`(?m)^USER\s+(\S+)\s*$`).FindAllStringSubmatch(readDockerfile(t), -1)
	if len(users) == 0 {
		t.Fatal("the Dockerfile sets no USER, so the image runs as root")
	}
	last := users[len(users)-1][1]
	uid, _, _ := strings.Cut(last, ":")
	if !regexp.MustCompile(`^\d+$`).MatchString(uid) {
		t.Fatalf("the final USER is %q; it must be numeric", last)
	}
	if uid == "0" {
		t.Fatal("the final USER is 0, which is root")
	}
}

// The HEALTHCHECK runs the subcommand main() dispatches on. Renaming either
// would leave a container reporting unhealthy forever, with a healthy
// service inside it.
func TestTheHealthcheckRunsTheSubcommandTheBinaryHas(t *testing.T) {
	t.Parallel()

	want := fmt.Sprintf(`CMD ["/api", %q]`, healthcheckCommand)
	if !strings.Contains(readDockerfile(t), want) {
		t.Errorf("the Dockerfile's HEALTHCHECK does not run %s", want)
	}
}

// The posture Kubernetes will enforce, held on the laptop first. A read-only
// root filesystem and no capabilities cost this binary nothing, and a later
// change that needs either should have to say so.
func TestTheServiceRunsWithoutPrivileges(t *testing.T) {
	t.Parallel()

	api := readCompose(t).Services["api"]
	if !api.ReadOnly {
		t.Error("the api service does not set read_only: true")
	}
	dropsAll := false
	for _, capability := range api.CapDrop {
		if strings.EqualFold(capability, "ALL") {
			dropsAll = true
		}
	}
	if !dropsAll {
		t.Error("the api service does not drop all capabilities (cap_drop: [ALL])")
	}
}

// Every base image is written out on its FROM line. Dependabot reads only a
// literal image there; behind an ARG it sees nothing, so the image is never
// offered an update and the pin ages without anything reporting it. That is
// the failure this guards against, and it is invisible until someone notices
// the base image is a year old.
func TestDependabotCanSeeEveryBaseImage(t *testing.T) {
	t.Parallel()

	froms := regexp.MustCompile(`(?mi)^FROM\s+(.*)$`).FindAllStringSubmatch(readDockerfile(t), -1)
	if len(froms) == 0 {
		t.Fatal("the Dockerfile has no FROM line")
	}
	for _, from := range froms {
		image := strings.Fields(from[1])[0]
		if strings.Contains(image, "$") {
			t.Errorf("FROM %s takes its image from a variable, which Dependabot cannot read", image)
			continue
		}
		if !strings.Contains(image, "@sha256:") {
			t.Errorf("FROM %s is not pinned by digest", image)
		}
	}
}
