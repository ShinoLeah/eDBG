package hostinstaller

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type ConfigLocation struct {
	Dir  string
	File string
}

type SpecialJSONStructure struct {
	TopKey    string
	NestedKey string
}

var clientAliases = map[string]string{
	"vscode":           "VS Code",
	"vs-code":          "VS Code",
	"vscode-insiders":  "VS Code Insiders",
	"vs-code-insiders": "VS Code Insiders",
	"claude-desktop":   "Claude",
	"claude-app":       "Claude",
	"claude-code":      "Claude Code",
	"roo":              "Roo Code",
	"roocode":          "Roo Code",
	"kilocode":         "Kilo Code",
	"kilo":             "Kilo Code",
	"gemini":           "Gemini CLI",
	"qwen":             "Qwen Coder",
	"copilot":          "Copilot CLI",
	"amazonq":          "Amazon Q",
	"amazon-q":         "Amazon Q",
	"lmstudio":         "LM Studio",
	"lm-studio":        "LM Studio",
	"augment":          "Augment Code",
	"qodo":             "Qodo Gen",
	"cline":            "Cline",
	"cursor":           "Cursor",
	"windsurf":         "Windsurf",
	"codex":            "Codex",
	"zed":              "Zed",
	"warp":             "Warp",
	"kiro":             "Kiro",
	"trae":             "Trae",
	"opencode":         "Opencode",
	"claude":           "Claude",
	"amazon q":         "Amazon Q",
	"gemini cli":       "Gemini CLI",
	"qwen coder":       "Qwen Coder",
	"copilot cli":      "Copilot CLI",
	"augment code":     "Augment Code",
	"roo code":         "Roo Code",
	"kilo code":        "Kilo Code",
	"vs code":          "VS Code",
	"vs code insiders": "VS Code Insiders",
}

var projectLevelConfigs = map[string]ConfigLocation{
	"Claude Code":      {Dir: "", File: ".mcp.json"},
	"Cursor":           {Dir: ".cursor", File: "mcp.json"},
	"VS Code":          {Dir: ".vscode", File: "mcp.json"},
	"VS Code Insiders": {Dir: ".vscode", File: "mcp.json"},
	"Windsurf":         {Dir: ".windsurf", File: "mcp.json"},
	"Zed":              {Dir: ".zed", File: "settings.json"},
}

var projectSpecialJSONStructures = map[string]SpecialJSONStructure{
	"VS Code":          {TopKey: "", NestedKey: "servers"},
	"VS Code Insiders": {TopKey: "", NestedKey: "servers"},
	"Zed":              {TopKey: "", NestedKey: "context_servers"},
}

var globalSpecialJSONStructures = map[string]SpecialJSONStructure{
	"VS Code":          {TopKey: "", NestedKey: "servers"},
	"VS Code Insiders": {TopKey: "", NestedKey: "servers"},
	"Opencode":         {TopKey: "", NestedKey: "mcp"},
	"Zed":              {TopKey: "", NestedKey: "context_servers"},
}

func GlobalConfigs() map[string]ConfigLocation {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support")
		return map[string]ConfigLocation{
			"Cline":            {Dir: filepath.Join(appSupport, "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings"), File: "cline_mcp_settings.json"},
			"Roo Code":         {Dir: filepath.Join(appSupport, "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "settings"), File: "mcp_settings.json"},
			"Kilo Code":        {Dir: filepath.Join(appSupport, "Code", "User", "globalStorage", "kilocode.kilo-code", "settings"), File: "mcp_settings.json"},
			"Claude":           {Dir: filepath.Join(appSupport, "Claude"), File: "claude_desktop_config.json"},
			"Claude Code":      {Dir: home, File: ".claude.json"},
			"Cursor":           {Dir: filepath.Join(home, ".cursor"), File: "mcp.json"},
			"Windsurf":         {Dir: filepath.Join(home, ".codeium", "windsurf"), File: "mcp_config.json"},
			"LM Studio":        {Dir: filepath.Join(home, ".lmstudio"), File: "mcp.json"},
			"Codex":            {Dir: filepath.Join(home, ".codex"), File: "config.toml"},
			"Zed":              {Dir: filepath.Join(appSupport, "Zed"), File: "settings.json"},
			"Gemini CLI":       {Dir: filepath.Join(home, ".gemini"), File: "settings.json"},
			"Qwen Coder":       {Dir: filepath.Join(home, ".qwen"), File: "settings.json"},
			"Copilot CLI":      {Dir: filepath.Join(home, ".copilot"), File: "mcp-config.json"},
			"Crush":            {Dir: home, File: "crush.json"},
			"Augment Code":     {Dir: filepath.Join(appSupport, "Code", "User"), File: "settings.json"},
			"Qodo Gen":         {Dir: filepath.Join(appSupport, "Code", "User"), File: "settings.json"},
			"Warp":             {Dir: filepath.Join(home, ".warp"), File: "mcp_config.json"},
			"Amazon Q":         {Dir: filepath.Join(home, ".aws", "amazonq"), File: "mcp.json"},
			"Opencode":         {Dir: filepath.Join(home, ".config", "opencode"), File: "opencode.json"},
			"Kiro":             {Dir: filepath.Join(home, ".kiro", "settings"), File: "mcp.json"},
			"Trae":             {Dir: filepath.Join(home, ".trae"), File: "mcp_config.json"},
			"VS Code":          {Dir: filepath.Join(appSupport, "Code", "User"), File: "mcp.json"},
			"VS Code Insiders": {Dir: filepath.Join(appSupport, "Code - Insiders", "User"), File: "mcp.json"},
		}
	case "linux":
		return map[string]ConfigLocation{
			"Cline":            {Dir: filepath.Join(home, ".config", "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings"), File: "cline_mcp_settings.json"},
			"Roo Code":         {Dir: filepath.Join(home, ".config", "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "settings"), File: "mcp_settings.json"},
			"Kilo Code":        {Dir: filepath.Join(home, ".config", "Code", "User", "globalStorage", "kilocode.kilo-code", "settings"), File: "mcp_settings.json"},
			"Claude Code":      {Dir: home, File: ".claude.json"},
			"Cursor":           {Dir: filepath.Join(home, ".cursor"), File: "mcp.json"},
			"Windsurf":         {Dir: filepath.Join(home, ".codeium", "windsurf"), File: "mcp_config.json"},
			"LM Studio":        {Dir: filepath.Join(home, ".lmstudio"), File: "mcp.json"},
			"Codex":            {Dir: filepath.Join(home, ".codex"), File: "config.toml"},
			"Zed":              {Dir: filepath.Join(home, ".config", "zed"), File: "settings.json"},
			"Gemini CLI":       {Dir: filepath.Join(home, ".gemini"), File: "settings.json"},
			"Qwen Coder":       {Dir: filepath.Join(home, ".qwen"), File: "settings.json"},
			"Copilot CLI":      {Dir: filepath.Join(home, ".copilot"), File: "mcp-config.json"},
			"Crush":            {Dir: home, File: "crush.json"},
			"Augment Code":     {Dir: filepath.Join(home, ".config", "Code", "User"), File: "settings.json"},
			"Qodo Gen":         {Dir: filepath.Join(home, ".config", "Code", "User"), File: "settings.json"},
			"Warp":             {Dir: filepath.Join(home, ".warp"), File: "mcp_config.json"},
			"Amazon Q":         {Dir: filepath.Join(home, ".aws", "amazonq"), File: "mcp.json"},
			"Opencode":         {Dir: filepath.Join(home, ".config", "opencode"), File: "opencode.json"},
			"Kiro":             {Dir: filepath.Join(home, ".kiro", "settings"), File: "mcp.json"},
			"Trae":             {Dir: filepath.Join(home, ".trae"), File: "mcp_config.json"},
			"VS Code":          {Dir: filepath.Join(home, ".config", "Code", "User"), File: "mcp.json"},
			"VS Code Insiders": {Dir: filepath.Join(home, ".config", "Code - Insiders", "User"), File: "mcp.json"},
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		return map[string]ConfigLocation{
			"Cline":            {Dir: filepath.Join(appData, "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings"), File: "cline_mcp_settings.json"},
			"Roo Code":         {Dir: filepath.Join(appData, "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "settings"), File: "mcp_settings.json"},
			"Kilo Code":        {Dir: filepath.Join(appData, "Code", "User", "globalStorage", "kilocode.kilo-code", "settings"), File: "mcp_settings.json"},
			"Claude":           {Dir: filepath.Join(appData, "Claude"), File: "claude_desktop_config.json"},
			"Claude Code":      {Dir: home, File: ".claude.json"},
			"Cursor":           {Dir: filepath.Join(home, ".cursor"), File: "mcp.json"},
			"Windsurf":         {Dir: filepath.Join(home, ".codeium", "windsurf"), File: "mcp_config.json"},
			"LM Studio":        {Dir: filepath.Join(home, ".lmstudio"), File: "mcp.json"},
			"Codex":            {Dir: filepath.Join(home, ".codex"), File: "config.toml"},
			"Zed":              {Dir: filepath.Join(appData, "Zed"), File: "settings.json"},
			"Gemini CLI":       {Dir: filepath.Join(home, ".gemini"), File: "settings.json"},
			"Qwen Coder":       {Dir: filepath.Join(home, ".qwen"), File: "settings.json"},
			"Copilot CLI":      {Dir: filepath.Join(home, ".copilot"), File: "mcp-config.json"},
			"Crush":            {Dir: home, File: "crush.json"},
			"Augment Code":     {Dir: filepath.Join(appData, "Code", "User"), File: "settings.json"},
			"Qodo Gen":         {Dir: filepath.Join(appData, "Code", "User"), File: "settings.json"},
			"Warp":             {Dir: filepath.Join(home, ".warp"), File: "mcp_config.json"},
			"Amazon Q":         {Dir: filepath.Join(home, ".aws", "amazonq"), File: "mcp.json"},
			"Opencode":         {Dir: filepath.Join(home, ".config", "opencode"), File: "opencode.json"},
			"Kiro":             {Dir: filepath.Join(home, ".kiro", "settings"), File: "mcp.json"},
			"Trae":             {Dir: filepath.Join(home, ".trae"), File: "mcp_config.json"},
			"VS Code":          {Dir: filepath.Join(appData, "Code", "User"), File: "mcp.json"},
			"VS Code Insiders": {Dir: filepath.Join(appData, "Code - Insiders", "User"), File: "mcp.json"},
		}
	default:
		return map[string]ConfigLocation{}
	}
}

func ProjectConfigs(projectDir string) map[string]ConfigLocation {
	result := map[string]ConfigLocation{}
	for name, spec := range projectLevelConfigs {
		dir := projectDir
		if spec.Dir != "" {
			dir = filepath.Join(projectDir, spec.Dir)
		}
		result[name] = ConfigLocation{
			Dir:  dir,
			File: spec.File,
		}
	}
	return result
}

func ProjectSpecialJSONStructures() map[string]SpecialJSONStructure {
	return projectSpecialJSONStructures
}

func GlobalSpecialJSONStructures() map[string]SpecialJSONStructure {
	return globalSpecialJSONStructures
}

func ResolveClientName(input string, available []string) string {
	lowerInput := strings.ToLower(strings.TrimSpace(input))
	for _, name := range available {
		if strings.ToLower(name) == lowerInput {
			return name
		}
	}
	if target, ok := clientAliases[lowerInput]; ok {
		for _, name := range available {
			if name == target {
				return name
			}
		}
	}
	match := ""
	for _, name := range available {
		if strings.Contains(strings.ToLower(name), lowerInput) {
			if match != "" {
				return ""
			}
			match = name
		}
	}
	return match
}
