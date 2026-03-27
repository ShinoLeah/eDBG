package main

import (
	"eDBG/hostinstaller"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var (
		doInstall   bool
		doUninstall bool
		showConfig  bool
		listClients bool
		project     bool
		projectDir  string
		clients     string
		serverURL   string
		serverName  string
	)

	flag.BoolVar(&doInstall, "install", false, "Install the eDBG MCP server into supported AI clients")
	flag.BoolVar(&doUninstall, "uninstall", false, "Remove the eDBG MCP server from supported AI clients")
	flag.BoolVar(&showConfig, "config", false, "Print example MCP configurations")
	flag.BoolVar(&listClients, "list-clients", false, "List supported AI clients for the selected scope")
	flag.BoolVar(&project, "project", false, "Use project-level MCP configuration when the client supports it")
	flag.StringVar(&projectDir, "project-dir", "", "Project directory used with --project (default: current directory)")
	flag.StringVar(&clients, "clients", "", "Comma-separated client names. Default: all supported clients in the selected scope")
	flag.StringVar(&serverURL, "url", "http://127.0.0.1:19810/mcp", "Forwarded eDBG MCP URL on the host")
	flag.StringVar(&serverName, "name", "edbg", "Server name written into client configs")
	flag.Parse()

	if doInstall && doUninstall {
		fail("choose either --install or --uninstall, not both")
	}

	serverURL = normalizeURL(serverURL)
	if project {
		if projectDir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				fail(fmt.Sprintf("failed to get current directory: %v", err))
			}
			projectDir = cwd
		}
		absProjectDir, err := filepath.Abs(projectDir)
		if err != nil {
			fail(fmt.Sprintf("failed to resolve project directory: %v", err))
		}
		projectDir = absProjectDir
	}

	targets := splitTargets(clients)
	options := hostinstaller.InstallOptions{
		ServerName: serverName,
		ServerURL:  serverURL,
		Project:    project,
		ProjectDir: projectDir,
	}

	switch {
	case showConfig:
		fmt.Println(hostinstaller.RenderExamples(serverName, serverURL))
	case listClients:
		for _, name := range hostinstaller.SupportedClients(project, projectDir) {
			fmt.Println(name)
		}
	case doInstall:
		results, err := hostinstaller.InstallAll(options, targets)
		if err != nil {
			fail(err.Error())
		}
		printResults(results, project, serverURL)
	case doUninstall:
		results, err := hostinstaller.UninstallAll(options, targets)
		if err != nil {
			fail(err.Error())
		}
		printResults(results, project, serverURL)
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  edbg-mcp-install --install [--clients codex,cursor]")
	fmt.Println("  edbg-mcp-install --uninstall [--clients codex]")
	fmt.Println("  edbg-mcp-install --list-clients")
	fmt.Println("  edbg-mcp-install --config")
	fmt.Println("")
	fmt.Println("Notes:")
	fmt.Println("  1. Start phone-side eDBG with --mcp.")
	fmt.Println("  2. Run adb forward tcp:19810 tcp:19810 (or any local host port you prefer).")
	fmt.Println("  3. Install client configs so they point to the forwarded MCP URL.")
}

func printResults(results []hostinstaller.InstallResult, project bool, serverURL string) {
	if len(results) == 0 {
		fmt.Println("No clients matched.")
		return
	}
	scope := "global"
	if project {
		scope = "project"
	}
	for _, result := range results {
		detected := "configured"
		if !project && !result.Detected {
			detected = "skipped (config dir not found)"
		}
		if result.Action == "installed" && !project && !result.Detected {
			fmt.Printf("%s: %s at %s\n", result.Client, detected, result.Path)
			continue
		}
		fmt.Printf("%s: %s %s config at %s\n", result.Client, result.Action, scope, result.Path)
	}
	fmt.Printf("\nMCP URL: %s\n", serverURL)
	if results[0].Action == "installed" {
		fmt.Println("Remember to keep phone-side eDBG running with --mcp and keep adb forward active.")
	}
}

func normalizeURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "http://127.0.0.1:19810/mcp"
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	if strings.HasSuffix(value, "/") {
		value = strings.TrimRight(value, "/")
	}
	if !strings.HasSuffix(value, "/mcp") {
		value += "/mcp"
	}
	return value
}

func splitTargets(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
