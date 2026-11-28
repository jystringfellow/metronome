package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/instance"
)

func chartExamples(t *testing.T, path, language string, count int) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rest := string(data)
	examples := make([]string, 0, count)
	for range count {
		_, after, found := strings.Cut(rest, "```"+language+"\n")
		if !found {
			t.Fatalf("missing %s example in %s", language, path)
		}
		example, remaining, found := strings.Cut(after, "```")
		if !found {
			t.Fatalf("unterminated example in %s", path)
		}
		examples = append(examples, example)
		rest = remaining
	}
	return examples
}

func chartValues(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func chartContains(t *testing.T, document string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(document, fragment) {
			t.Errorf("missing %q in:\n%s", fragment, document)
		}
	}
}

func chartOmits(t *testing.T, document string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(document, fragment) {
			t.Errorf("unexpected %q in:\n%s", fragment, document)
		}
	}
}

func chartDocuments(t *testing.T, output string) map[string]string {
	t.Helper()
	documents := map[string]string{}
	for _, document := range strings.Split(output, "\n---\n") {
		kind := ""
		for _, line := range strings.Split(document, "\n") {
			if value, found := strings.CutPrefix(line, "kind: "); found {
				kind = value
				break
			}
		}
		if kind == "" {
			continue
		}
		if _, exists := documents[kind]; exists {
			t.Fatalf("duplicate manifest kind %s", kind)
		}
		if kind == "Secret" {
			t.Fatal("chart must reference, not create, Secrets")
		}
		documents[kind] = document
	}
	return documents
}

func chartKinds(t *testing.T, documents map[string]string, kinds ...string) {
	t.Helper()
	if len(documents) != len(kinds) {
		t.Errorf("expected %d manifests, got %d", len(kinds), len(documents))
	}
	for _, kind := range kinds {
		if documents[kind] == "" {
			t.Errorf("missing %s manifest", kind)
		}
	}
}

func chartConfig(t *testing.T, document string) string {
	t.Helper()
	_, rest, found := strings.Cut(document, "  instance.json: |\n")
	if !found {
		t.Fatalf("missing instance.json block:\n%s", document)
	}
	var lines []string
	for _, line := range strings.Split(rest, "\n") {
		if value, found := strings.CutPrefix(line, "    "); found {
			lines = append(lines, value)
		} else {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func chartChecksum(t *testing.T, document string) string {
	t.Helper()
	for _, line := range strings.Split(document, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "checksum/config: "); found {
			return value
		}
	}
	t.Fatal("missing config checksum")
	return ""
}

func TestHelmChart(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("METRONOME_TEST_HELM") == "1" {
			t.Fatal("METRONOME_TEST_HELM=1 requires helm on PATH")
		}
		t.Skip("helm not installed; set METRONOME_TEST_HELM=1 to require it")
	}
	const chart = "../../deploy/helm/metronome"
	examples := chartExamples(t, "../../docs/kubernetes.md", "yaml", 3)
	postgres := chartValues(t, examples[0])
	sqlite := chartValues(t, examples[1])
	proxy := chartValues(t, examples[2])
	render := func(args ...string) (string, error) {
		output, err := exec.Command(helm, append([]string{"template", "test", chart}, args...)...).CombinedOutput()
		return string(output), err
	}
	mustRender := func(t *testing.T, args ...string) map[string]string {
		t.Helper()
		output, err := render(args...)
		if err != nil {
			t.Fatalf("helm template: %v\n%s", err, output)
		}
		return chartDocuments(t, output)
	}
	refuse := func(t *testing.T, expected string, args ...string) {
		t.Helper()
		output, err := render(args...)
		if err == nil || !strings.Contains(output, expected) {
			t.Fatalf("expected template error containing %q, got %v:\n%s", expected, err, output)
		}
	}

	t.Run("lint", func(t *testing.T) {
		for _, values := range []string{postgres, sqlite} {
			if output, err := exec.Command(helm, "lint", "--strict", chart, "-f", values).CombinedOutput(); err != nil {
				t.Fatalf("helm lint: %v\n%s", err, output)
			}
		}
	})
	t.Run("defaults refuse", func(t *testing.T) {
		refuse(t, "image.tag")
		refuse(t, "database.postgres.existingSecret", "--set", "image.tag=1.4.2")
		refuse(t, "database.type must be postgres or sqlite", "-f", postgres, "--set", "database.type=mysql")
		refuse(t, "actor must be nonempty", "-f", postgres, "--set", "actor=")
	})
	t.Run("postgres", func(t *testing.T) {
		documents := mustRender(t, "-f", postgres, "--set", "database.sqlite.persistence.existingClaim=ignored")
		chartKinds(t, documents, "Deployment", "Service", "ConfigMap", "ServiceAccount")
		deployment := documents["Deployment"]
		chartContains(t, deployment, "replicas: 1", "type: Recreate", "- --db-env\n", "- METRONOME_DB\n",
			"- --config\n", "/etc/metronome/instance.json", "secretKeyRef:", `name: "metronome-db"`, `key: "url"`,
			"startupProbe:", "livenessProbe:", "readinessProbe:", "path: /healthz", "path: /readyz", "port: http",
			`image: "ghcr.io/jystringfellow/metronome:1.4.2"`, "name: METRONOME_ACTOR", `value: "metronome"`,
			"readOnlyRootFilesystem: true", "runAsUser: 65532", "automountServiceAccountToken: false",
			"mountPath: /etc/metronome", "readOnly: true", "mountPath: /tmp", "mountPath: /home/metronome")
		chartOmits(t, deployment, "- --db\n", "name: data\n", "mountPath: /data", "persistentVolumeClaim:", "command:")
		chartContains(t, documents["Service"], "type: ClusterIP", "targetPort: http")
	})
	t.Run("sqlite", func(t *testing.T) {
		documents := mustRender(t, "-f", sqlite)
		chartKinds(t, documents, "Deployment", "Service", "ConfigMap", "ServiceAccount", "PersistentVolumeClaim")
		chartContains(t, documents["PersistentVolumeClaim"], "helm.sh/resource-policy: keep", "ReadWriteOnce", "storage: 1Gi")
		chartOmits(t, documents["PersistentVolumeClaim"], "storageClassName:")
		chartContains(t, documents["Deployment"], "- --db\n", "- /data/metronome.db", "mountPath: /data", "claimName: test-metronome-data")
		chartOmits(t, documents["Deployment"], "METRONOME_DB", "--db-env")
		documents = mustRender(t, "-f", sqlite, "--set", "database.sqlite.persistence.existingClaim=example-data")
		chartKinds(t, documents, "Deployment", "Service", "ConfigMap", "ServiceAccount")
		chartContains(t, documents["Deployment"], "claimName: example-data")
	})
	t.Run("null maps", func(t *testing.T) {
		documents := mustRender(t, "-f", postgres, "--set", "podAnnotations=null", "--set", "podLabels=null")
		chartContains(t, documents["Deployment"], "checksum/config:", "app.kubernetes.io/name: metronome")
		documents = mustRender(t, "-f", sqlite, "--set", "database.sqlite.persistence.annotations=null")
		chartContains(t, documents["PersistentVolumeClaim"], "helm.sh/resource-policy: keep")
	})
	t.Run("config", func(t *testing.T) {
		example := chartExamples(t, "../../docs/configuration.md", "json", 1)[0]
		values := func(t *testing.T, config string) string {
			return chartValues(t, "config: "+strings.ReplaceAll(strings.TrimSpace(config), "\n", "\n  ")+"\n")
		}
		check := func(t *testing.T, config string) string {
			t.Helper()
			documents := mustRender(t, "-f", sqlite, "-f", values(t, config))
			rendered := chartConfig(t, documents["ConfigMap"])
			if _, err := instance.Parse(strings.NewReader(rendered)); err != nil {
				t.Fatalf("invalid rendered instance config: %v\n%s", err, rendered)
			}
			var source, actual any
			if err := json.Unmarshal([]byte(config), &source); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(rendered), &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(source, actual) {
				t.Fatalf("rendered config differs from source:\n%s\n%s", config, rendered)
			}
			return chartChecksum(t, documents["Deployment"])
		}
		original := check(t, example)
		changed := check(t, strings.Replace(example, `"batch": 100`, `"batch": 1000`, 1))
		if original == changed {
			t.Fatal("config change did not change pod checksum")
		}
	})
	t.Run("secrets", func(t *testing.T) {
		documents := mustRender(t, "-f", postgres, "--set", "envFromSecrets[0]=example-secrets", "--set", "database.postgres.key=connection")
		chartContains(t, documents["Deployment"], `name: "GH_TOKEN"`, "secretKeyRef:", `name: "metronome-github"`, `key: "token"`,
			"secretRef:", `name: "example-secrets"`, `key: "connection"`)
	})
	t.Run("ingress", func(t *testing.T) {
		refuse(t, "enable authProxy", "-f", postgres, "--set", "ingress.enabled=true")
		documents := mustRender(t, "-f", postgres, "--set", "ingress.enabled=true,ingress.allowWithoutProxy=true")
		chartKinds(t, documents, "Deployment", "Service", "ConfigMap", "ServiceAccount", "Ingress")
		chartContains(t, documents["Ingress"], `host: "metronome.example.com"`, "name: test-metronome", "name: http")
		refuse(t, "authProxy.image.repository", "-f", postgres, "--set", "authProxy.enabled=true")
		documents = mustRender(t, "-f", postgres, "-f", proxy)
		chartKinds(t, documents, "Deployment", "Service", "ConfigMap", "ServiceAccount", "Ingress")
		chartContains(t, documents["Deployment"], "- name: metronome\n", "- name: auth-proxy\n", "containerPort: 4180",
			"--upstream=http://127.0.0.1:7340", `name: "OAUTH2_PROXY_CLIENT_SECRET"`, `name: "metronome-proxy"`, "port: http")
		chartOmits(t, documents["Deployment"], "port: proxy\n")
		chartContains(t, documents["Service"], "targetPort: proxy")
		chartContains(t, documents["Ingress"], "secretName: metronome-tls")
	})
	t.Run("digest", func(t *testing.T) {
		digest := "sha256:" + strings.Repeat("a", 64)
		documents := mustRender(t, "-f", postgres, "--set", "image.digest="+digest)
		chartContains(t, documents["Deployment"], `image: "ghcr.io/jystringfellow/metronome@`+digest+`"`)
		chartOmits(t, documents["Deployment"], "ghcr.io/jystringfellow/metronome:1.4.2")
	})
}
