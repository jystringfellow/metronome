package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerImage(t *testing.T) {
	contents, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var bases []string
	final := map[string]string{}
	for _, line := range strings.Split(string(contents), "\n") {
		instruction, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		if instruction == "FROM" {
			fields := strings.Fields(value)
			if strings.HasPrefix(fields[0], "--platform=") {
				fields = fields[1:]
			}
			bases = append(bases, fields[0])
			final = map[string]string{}
		}
		final[instruction] = value
	}
	file := string(contents)
	checked := strings.Index(file, "sha256sum -c gh.sha256")
	downloaded := strings.Index(file, "curl -fsSLO")
	extracted := strings.Index(file, "tar -xzf")
	tests := []struct {
		name string
		ok   bool
	}{
		{"uses bundled Dockerfile frontend", !strings.Contains(file, "# syntax=")},
		{"cross-compiled static executable", strings.Contains(file, "CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH") && strings.Contains(file, "go build -trimpath -ldflags='-s -w' -o /out/metronome ./cmd/metronome")},
		{"final stage runs as non-root", final["USER"] == "65532:65532"},
		{"entrypoint forwards signals to metronome", final["ENTRYPOINT"] == `["/sbin/tini", "--", "metronome"]`},
		{"server uses the data directory", final["CMD"] == `["serve", "--addr", "0.0.0.0:7340", "--db", "/data/metronome.db"]`},
		{"gh verified before extraction", downloaded >= 0 && checked > downloaded && extracted > checked},
		{"base images declared", len(bases) == 3},
	}
	for _, base := range bases {
		tests = append(tests, struct {
			name string
			ok   bool
		}{"versioned base " + base, strings.Contains(base, ":") && !strings.HasSuffix(base, ":latest")})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !test.ok {
				t.Fatal(test.name + " check failed")
			}
		})
	}
}

func TestContainerBuild(t *testing.T) {
	if os.Getenv("METRONOME_TEST_IMAGE_BUILD") != "1" {
		t.Skip("set METRONOME_TEST_IMAGE_BUILD=1 to build the native and multi-platform images")
	}
	for _, test := range []struct {
		name   string
		args   []string
		format string
	}{
		{"native", []string{"build"}, "docker"},
		{"multi-platform", []string{"buildx", "build", "--platform", "linux/amd64,linux/arm64", "--sbom=true", "--provenance=mode=max"}, "oci"},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "image.tar")
			args := append(test.args, "--progress=plain", "--output", "type="+test.format+",dest="+archive, ".")
			command := exec.Command("docker", args...)
			command.Dir = "../.."
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("docker build failed: %v\n%s", err, output)
			}
		})
	}
}
