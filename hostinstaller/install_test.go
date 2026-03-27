package hostinstaller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGlobalJSONInstallShapes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	serverName := "edbg"
	serverURL := "http://127.0.0.1:19810/mcp"
	structures := GlobalSpecialJSONStructures()

	cases := []struct {
		client         string
		path           []string
		expectType     string
		expectURL      string
		expectURLKey   string
		expectTypeOmit bool
	}{
		{client: "Claude", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Claude Code", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Cursor", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Cline", path: []string{"mcpServers", serverName}, expectType: "streamableHttp", expectURL: serverURL},
		{client: "Roo Code", path: []string{"mcpServers", serverName}, expectType: "streamable-http", expectURL: serverURL},
		{client: "Kilo Code", path: []string{"mcpServers", serverName}, expectType: "streamable-http", expectURL: serverURL},
		{client: "Windsurf", path: []string{"mcpServers", serverName}, expectURLKey: "serverUrl", expectURL: serverURL, expectTypeOmit: true},
		{client: "Zed", path: []string{"context_servers", serverName}, expectURL: serverURL, expectTypeOmit: true},
		{client: "LM Studio", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Gemini CLI", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Qwen Coder", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Copilot CLI", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Crush", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Augment Code", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Qodo Gen", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Warp", path: []string{"mcpServers", serverName}, expectURL: serverURL, expectTypeOmit: true},
		{client: "Amazon Q", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Kiro", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Trae", path: []string{"mcpServers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "VS Code", path: []string{"servers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "VS Code Insiders", path: []string{"servers", serverName}, expectType: "http", expectURL: serverURL},
		{client: "Opencode", path: []string{"mcp", serverName}, expectType: "remote", expectURL: serverURL},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.client, func(t *testing.T) {
			t.Parallel()

			configPath := filepath.Join(tmpDir, strings.ReplaceAll(tc.client, " ", "_")+".json")
			options := InstallOptions{ServerName: serverName, ServerURL: serverURL}
			if err := installJSON(configPath, tc.client, options, structures); err != nil {
				t.Fatalf("installJSON failed: %v", err)
			}

			root := readJSONFile(t, configPath)
			server := mustNestedMap(t, root, tc.path...)

			urlKey := tc.expectURLKey
			if urlKey == "" {
				urlKey = "url"
			}
			if got := server[urlKey]; got != tc.expectURL {
				t.Fatalf("unexpected url: got %v want %s", got, tc.expectURL)
			}
			if tc.expectTypeOmit {
				if _, ok := server["type"]; ok {
					t.Fatalf("type should be omitted, got %v", server["type"])
				}
			} else if got := server["type"]; got != tc.expectType {
				t.Fatalf("unexpected type: got %v want %s", got, tc.expectType)
			}
		})
	}
}

func TestProjectJSONInstallShapes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	serverName := "edbg"
	serverURL := "http://127.0.0.1:19810/mcp"
	structures := ProjectSpecialJSONStructures()

	cases := []struct {
		client       string
		path         []string
		expectURLKey string
		expectType   string
		typeOptional bool
	}{
		{client: "Claude Code", path: []string{"mcpServers", serverName}, expectType: "http"},
		{client: "Cursor", path: []string{"mcpServers", serverName}, expectType: "http"},
		{client: "VS Code", path: []string{"servers", serverName}, expectType: "http"},
		{client: "VS Code Insiders", path: []string{"servers", serverName}, expectType: "http"},
		{client: "Windsurf", path: []string{"mcpServers", serverName}, expectURLKey: "serverUrl", typeOptional: true},
		{client: "Zed", path: []string{"context_servers", serverName}, typeOptional: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.client, func(t *testing.T) {
			t.Parallel()

			configPath := filepath.Join(tmpDir, strings.ReplaceAll(tc.client, " ", "_")+".json")
			options := InstallOptions{ServerName: serverName, ServerURL: serverURL}
			if err := installJSON(configPath, tc.client, options, structures); err != nil {
				t.Fatalf("installJSON failed: %v", err)
			}

			root := readJSONFile(t, configPath)
			server := mustNestedMap(t, root, tc.path...)

			urlKey := tc.expectURLKey
			if urlKey == "" {
				urlKey = "url"
			}
			if got := server[urlKey]; got != serverURL {
				t.Fatalf("unexpected url: got %v want %s", got, serverURL)
			}
			if tc.typeOptional {
				if _, ok := server["type"]; ok {
					t.Fatalf("type should be omitted, got %v", server["type"])
				}
				return
			}
			if got := server["type"]; got != tc.expectType {
				t.Fatalf("unexpected type: got %v want %s", got, tc.expectType)
			}
		})
	}
}

func TestCodexTomlFallbackBlock(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	content := "model = \"gpt-5.4\"\n\n[mcp_servers.existing]\nurl = \"http://127.0.0.1:9999/mcp\"\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := installCodexToml(configPath, "edbg", "http://127.0.0.1:19810/mcp"); err != nil {
		t.Fatalf("installCodexToml failed: %v", err)
	}

	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(updated)
	if !strings.Contains(text, "[mcp_servers.existing]") {
		t.Fatalf("existing server entry was removed: %s", text)
	}
	if !strings.Contains(text, "[mcp_servers.edbg]") {
		t.Fatalf("edbg server entry missing: %s", text)
	}
	if !strings.Contains(text, "url = \"http://127.0.0.1:19810/mcp\"") {
		t.Fatalf("edbg url missing: %s", text)
	}
}

func TestDefaultServerEntryMatrix(t *testing.T) {
	t.Parallel()

	serverURL := "http://127.0.0.1:19810/mcp"
	cases := []struct {
		client         string
		expectType     string
		expectURL      string
		expectURLKey   string
		expectTypeOmit bool
	}{
		{client: "Codex", expectURL: serverURL, expectTypeOmit: true},
		{client: "Opencode", expectType: "remote", expectURL: serverURL},
		{client: "Cline", expectType: "streamableHttp", expectURL: serverURL},
		{client: "Roo Code", expectType: "streamable-http", expectURL: serverURL},
		{client: "Kilo Code", expectType: "streamable-http", expectURL: serverURL},
		{client: "Claude", expectType: "http", expectURL: serverURL},
		{client: "Claude Code", expectType: "http", expectURL: serverURL},
		{client: "Cursor", expectType: "http", expectURL: serverURL},
		{client: "Windsurf", expectURLKey: "serverUrl", expectURL: serverURL, expectTypeOmit: true},
		{client: "LM Studio", expectType: "http", expectURL: serverURL},
		{client: "Gemini CLI", expectType: "http", expectURL: serverURL},
		{client: "Qwen Coder", expectType: "http", expectURL: serverURL},
		{client: "Copilot CLI", expectType: "http", expectURL: serverURL},
		{client: "Crush", expectType: "http", expectURL: serverURL},
		{client: "Augment Code", expectType: "http", expectURL: serverURL},
		{client: "Qodo Gen", expectType: "http", expectURL: serverURL},
		{client: "Warp", expectURL: serverURL, expectTypeOmit: true},
		{client: "Amazon Q", expectType: "http", expectURL: serverURL},
		{client: "Kiro", expectType: "http", expectURL: serverURL},
		{client: "Trae", expectType: "http", expectURL: serverURL},
		{client: "VS Code", expectType: "http", expectURL: serverURL},
		{client: "VS Code Insiders", expectType: "http", expectURL: serverURL},
		{client: "Zed", expectURL: serverURL, expectTypeOmit: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.client, func(t *testing.T) {
			t.Parallel()

			entry := defaultServerEntry(tc.client, serverURL)
			urlKey := tc.expectURLKey
			if urlKey == "" {
				urlKey = "url"
			}
			if got := entry[urlKey]; got != tc.expectURL {
				t.Fatalf("unexpected url: got %v want %s", got, tc.expectURL)
			}
			if tc.expectTypeOmit {
				if _, ok := entry["type"]; ok {
					t.Fatalf("type should be omitted, got %v", entry["type"])
				}
				return
			}
			if got := entry["type"]; got != tc.expectType {
				t.Fatalf("unexpected type: got %v want %s", got, tc.expectType)
			}
		})
	}
}

func TestGlobalConfigLocationFixups(t *testing.T) {
	t.Parallel()

	configs := GlobalConfigs()

	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		if got := configs["VS Code"].File; got != "mcp.json" {
			t.Fatalf("VS Code global config file = %q, want mcp.json", got)
		}
		if got := configs["VS Code Insiders"].File; got != "mcp.json" {
			t.Fatalf("VS Code Insiders global config file = %q, want mcp.json", got)
		}
		if got := configs["Amazon Q"].File; got != "mcp.json" {
			t.Fatalf("Amazon Q config file = %q, want mcp.json", got)
		}
		if got := configs["Kiro"].File; got != "mcp.json" {
			t.Fatalf("Kiro config file = %q, want mcp.json", got)
		}
		if !strings.Contains(filepath.ToSlash(configs["Kiro"].Dir), "/.kiro/settings") {
			t.Fatalf("Kiro config dir = %q, want path containing /.kiro/settings", configs["Kiro"].Dir)
		}
	default:
		t.Skip("unsupported runtime")
	}
}

func TestRenderExamplesUsesCurrentVSCodeShape(t *testing.T) {
	t.Parallel()

	output := RenderExamples("edbg", "http://127.0.0.1:19810/mcp")
	if strings.Contains(output, "settings.json style") {
		t.Fatalf("RenderExamples still references VS Code settings.json: %s", output)
	}
	if !strings.Contains(output, "VS Code mcp.json style JSON") {
		t.Fatalf("RenderExamples missing VS Code mcp.json heading: %s", output)
	}
	if !strings.Contains(output, "\"servers\"") {
		t.Fatalf("RenderExamples missing VS Code servers object: %s", output)
	}
}

func readJSONFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	root := map[string]interface{}{}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return root
}

func mustNestedMap(t *testing.T, root map[string]interface{}, path ...string) map[string]interface{} {
	t.Helper()

	current := root
	for _, key := range path {
		value, ok := current[key]
		if !ok {
			t.Fatalf("missing key %q in path %v", key, path)
		}
		next, ok := value.(map[string]interface{})
		if !ok {
			t.Fatalf("key %q in path %v is not an object", key, path)
		}
		current = next
	}
	return current
}
