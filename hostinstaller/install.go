package hostinstaller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	ManagedTomlBegin = "# BEGIN EDBG MCP"
	ManagedTomlEnd   = "# END EDBG MCP"
)

type InstallOptions struct {
	ServerName string
	ServerURL  string
	Project    bool
	ProjectDir string
}

type InstallResult struct {
	Client   string
	Path     string
	Action   string
	Detected bool
}

func SupportedClients(project bool, projectDir string) []string {
	configs := configsForScope(project, projectDir)
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func InstallAll(options InstallOptions, targets []string) ([]InstallResult, error) {
	return mutateAll(options, targets, true)
}

func UninstallAll(options InstallOptions, targets []string) ([]InstallResult, error) {
	return mutateAll(options, targets, false)
}

func RenderExamples(serverName string, serverURL string) string {
	generic := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			serverName: defaultServerEntry("Claude", serverURL),
		},
	}
	opencode := map[string]interface{}{
		"mcp": map[string]interface{}{
			serverName: defaultServerEntry("Opencode", serverURL),
		},
	}
	vscode := map[string]interface{}{
		"servers": map[string]interface{}{
			serverName: defaultServerEntry("VS Code", serverURL),
		},
	}

	var builder strings.Builder
	builder.WriteString("Claude / Cursor / Claude Code style JSON:\n")
	builder.WriteString(prettyJSON(generic))
	builder.WriteString("\n\nVS Code mcp.json style JSON:\n")
	builder.WriteString(prettyJSON(vscode))
	builder.WriteString("\n\nOpencode style JSON:\n")
	builder.WriteString(prettyJSON(opencode))
	builder.WriteString("\n\nCodex config.toml snippet:\n")
	builder.WriteString(renderCodexTomlBlock(serverName, serverURL))
	return builder.String()
}

func mutateAll(options InstallOptions, targets []string, install bool) ([]InstallResult, error) {
	configs := configsForScope(options.Project, options.ProjectDir)
	if len(configs) == 0 {
		return nil, fmt.Errorf("no supported clients for this scope")
	}

	targetNames, err := normalizeTargets(targets, options.Project, options.ProjectDir)
	if err != nil {
		return nil, err
	}

	results := make([]InstallResult, 0, len(targetNames))
	for _, client := range targetNames {
		location := configs[client]
		result := InstallResult{
			Client:   client,
			Path:     filepath.Join(location.Dir, location.File),
			Detected: pathDetected(location, options.Project),
		}
		if install {
			err = installClient(location, client, options)
			if err != nil {
				return results, fmt.Errorf("%s: %w", client, err)
			}
			result.Action = "installed"
		} else {
			err = uninstallClient(location, client, options)
			if err != nil {
				return results, fmt.Errorf("%s: %w", client, err)
			}
			result.Action = "removed"
		}
		results = append(results, result)
	}
	return results, nil
}

func normalizeTargets(targets []string, project bool, projectDir string) ([]string, error) {
	available := SupportedClients(project, projectDir)
	if len(targets) == 0 {
		return available, nil
	}

	seen := map[string]bool{}
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		name := ResolveClientName(target, available)
		if name == "" {
			return nil, fmt.Errorf("unknown or ambiguous client %q", target)
		}
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result, nil
}

func configsForScope(project bool, projectDir string) map[string]ConfigLocation {
	if project {
		return ProjectConfigs(projectDir)
	}
	return GlobalConfigs()
}

func specialStructuresForScope(project bool) map[string]SpecialJSONStructure {
	if project {
		return ProjectSpecialJSONStructures()
	}
	return GlobalSpecialJSONStructures()
}

func pathDetected(location ConfigLocation, project bool) bool {
	if project {
		return true
	}
	if _, err := os.Stat(location.Dir); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(location.Dir, location.File)); err == nil {
		return true
	}
	return false
}

func installClient(location ConfigLocation, client string, options InstallOptions) error {
	if !options.Project {
		if _, err := os.Stat(location.Dir); err != nil && os.IsNotExist(err) {
			return nil
		}
	}
	if err := os.MkdirAll(location.Dir, 0o755); err != nil {
		return err
	}
	if client == "Codex" {
		return installCodex(filepath.Join(location.Dir, location.File), options.ServerName, options.ServerURL)
	}
	return installJSON(filepath.Join(location.Dir, location.File), client, options, specialStructuresForScope(options.Project))
}

func uninstallClient(location ConfigLocation, client string, options InstallOptions) error {
	target := filepath.Join(location.Dir, location.File)
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if client == "Codex" {
		return uninstallCodex(target, options.ServerName)
	}
	return uninstallJSON(target, client, options.ServerName, specialStructuresForScope(options.Project))
}

func installJSON(path string, client string, options InstallOptions, structures map[string]SpecialJSONStructure) error {
	root, err := readJSONObject(path)
	if err != nil {
		return err
	}

	servers, err := jsonServersView(root, client, structures)
	if err != nil {
		return err
	}
	servers[options.ServerName] = defaultServerEntry(client, options.ServerURL)

	return writeJSONObject(path, root)
}

func uninstallJSON(path string, client string, serverName string, structures map[string]SpecialJSONStructure) error {
	root, err := readJSONObject(path)
	if err != nil {
		return err
	}

	servers, err := jsonServersView(root, client, structures)
	if err != nil {
		return err
	}
	delete(servers, serverName)
	return writeJSONObject(path, root)
}

func jsonServersView(root map[string]interface{}, client string, structures map[string]SpecialJSONStructure) (map[string]interface{}, error) {
	spec, ok := structures[client]
	if !ok {
		spec = SpecialJSONStructure{TopKey: "", NestedKey: "mcpServers"}
	}

	current := root
	if spec.TopKey != "" {
		current = ensureJSONObject(current, spec.TopKey)
	}
	if spec.NestedKey == "" {
		return current, nil
	}
	return ensureJSONObject(current, spec.NestedKey), nil
}

func defaultServerEntry(client string, serverURL string) map[string]interface{} {
	switch client {
	case "Codex":
		return map[string]interface{}{
			"url": serverURL,
		}
	case "Opencode":
		return map[string]interface{}{
			"type": "remote",
			"url":  serverURL,
		}
	case "Cline":
		return map[string]interface{}{
			"type": "streamableHttp",
			"url":  serverURL,
		}
	case "Roo Code", "Kilo Code":
		return map[string]interface{}{
			"type": "streamable-http",
			"url":  serverURL,
		}
	case "Windsurf":
		return map[string]interface{}{
			"serverUrl": serverURL,
		}
	case "Warp", "Zed":
		return map[string]interface{}{
			"url": serverURL,
		}
	default:
		return map[string]interface{}{
			"type": "http",
			"url":  serverURL,
		}
	}
}

func readJSONObject(path string) (map[string]interface{}, error) {
	root := map[string]interface{}{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return root, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return root, nil
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	return root, nil
}

func writeJSONObject(path string, root map[string]interface{}) error {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func ensureJSONObject(parent map[string]interface{}, key string) map[string]interface{} {
	if existing, ok := parent[key]; ok {
		if typed, ok := existing.(map[string]interface{}); ok {
			return typed
		}
	}
	child := map[string]interface{}{}
	parent[key] = child
	return child
}

func installCodexToml(path string, serverName string, serverURL string) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	updated := removeCodexTomlSection(string(content), serverName)
	block := renderCodexTomlBlock(serverName, serverURL)
	updated = strings.TrimRight(updated, "\n")
	if updated != "" {
		updated += "\n\n"
	}
	updated += block + "\n"
	return os.WriteFile(path, []byte(updated), 0o644)
}

func installCodex(path string, serverName string, serverURL string) error {
	if shouldUseCodexCLI(path) {
		if err := runCodexCLI("mcp", "add", serverName, "--url", serverURL); err != nil {
			return err
		}
		return nil
	}
	return installCodexToml(path, serverName, serverURL)
}

func uninstallCodexToml(path string, serverName string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated := strings.TrimSpace(removeCodexTomlSection(string(content), serverName))
	if updated == "" {
		updated = ""
	} else {
		updated += "\n"
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

func uninstallCodex(path string, serverName string) error {
	if shouldUseCodexCLI(path) {
		if err := runCodexCLI("mcp", "remove", serverName); err != nil {
			return err
		}
		return nil
	}
	return uninstallCodexToml(path, serverName)
}

func shouldUseCodexCLI(configPath string) bool {
	if _, err := exec.LookPath("codex"); err != nil {
		return false
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	defaultPath := filepath.Join(home, ".codex", "config.toml")
	return samePath(configPath, defaultPath)
}

func samePath(left string, right string) bool {
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func runCodexCLI(args ...string) error {
	cmd := exec.Command("codex", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("codex %s failed: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("codex %s failed: %s", strings.Join(args, " "), message)
	}
	return nil
}

func removeCodexTomlSection(content string, serverName string) string {
	lines := strings.Split(content, "\n")
	filtered := make([]string, 0, len(lines))

	inManagedBlock := false
	inServerBlock := false
	serverHeader := fmt.Sprintf("[mcp_servers.%s]", serverName)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == ManagedTomlBegin {
			inManagedBlock = true
			continue
		}
		if trimmed == ManagedTomlEnd {
			inManagedBlock = false
			continue
		}
		if inManagedBlock {
			continue
		}

		if trimmed == serverHeader {
			inServerBlock = true
			continue
		}
		if inServerBlock {
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				inServerBlock = false
			} else {
				continue
			}
		}
		filtered = append(filtered, line)
	}

	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func renderCodexTomlBlock(serverName string, serverURL string) string {
	return fmt.Sprintf(
		"%s\n[mcp_servers.%s]\nurl = %q\n%s",
		ManagedTomlBegin,
		serverName,
		serverURL,
		ManagedTomlEnd,
	)
}

func prettyJSON(value interface{}) string {
	buf := &bytes.Buffer{}
	encoder := json.NewEncoder(buf)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
	return strings.TrimSpace(buf.String())
}
