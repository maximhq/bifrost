// Package lib provides plugin compilation and loading functionality for Bifrost HTTP transport.
package lib

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"plugin"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// PluginCompiler handles just-in-time compilation of plugins
type PluginCompiler struct {
	tempDir         string
	compiledPlugins map[string]string // plugin name -> .so file path
	sessionID       string            // unique session for cleanup
}

// NewPluginCompiler creates a new plugin compiler instance
func NewPluginCompiler() *PluginCompiler {
	sessionID := fmt.Sprintf("bifrost-plugins-%d", time.Now().Unix())
	tempDir := filepath.Join(os.TempDir(), sessionID)

	if err := os.MkdirAll(tempDir, 0755); err != nil {
		log.Printf("warning: failed to create plugin temp directory: %v", err)
	}

	return &PluginCompiler{
		tempDir:         tempDir,
		compiledPlugins: make(map[string]string),
		sessionID:       sessionID,
	}
}

// LoadPlugin handles the complete workflow for a single plugin
func (pc *PluginCompiler) LoadPlugin(config PluginConfig) (schemas.Plugin, error) {
	log.Printf("loading plugin %s from %s", config.Name, config.Source)

	// 1. Setup workspace
	workDir := filepath.Join(pc.tempDir, config.Name)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}

	// 2. Get source code
	var err error
	switch config.Type {
	case "remote":
		err = pc.downloadRemotePlugin(workDir, config.Source)
	case "local":
		err = pc.copyLocalPlugin(workDir, config.Source)
	default:
		return nil, fmt.Errorf("unsupported plugin type: %s", config.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get plugin source: %w", err)
	}

	// 3. Compile plugin
	soPath, err := pc.compilePlugin(workDir, config.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to compile plugin: %w", err)
	}

	// 4. Load compiled plugin
	pluginInstance, err := pc.loadCompiledPlugin(soPath, config.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to load compiled plugin: %w", err)
	}

	pc.compiledPlugins[config.Name] = soPath
	return pluginInstance, nil
}

// downloadRemotePlugin downloads a remote Go module plugin
func (pc *PluginCompiler) downloadRemotePlugin(workDir, modulePath string) error {
	log.Printf("downloading remote plugin: %s", modulePath)

	// Initialize a new Go module in the workspace
	cmd := exec.Command("go", "mod", "init", "plugin-temp")
	cmd.Dir = workDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod init failed: %s", string(output))
	}

	// Get the plugin module
	cmd = exec.Command("go", "get", modulePath)
	cmd.Dir = workDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go get failed: %s", string(output))
	}

	// Note: We rely on go mod tidy to resolve compatible versions

	// Create a simple main.go that re-exports the plugin
	mainContent := fmt.Sprintf(`package main

import (
	"encoding/json"
	"github.com/maximhq/bifrost/core/schemas"
	plugin "%s"
)

// Re-export the Init function
func Init(config json.RawMessage) (schemas.Plugin, error) {
	return plugin.Init(config)
}

func main() {}
`, modulePath)

	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte(mainContent), 0644); err != nil {
		return fmt.Errorf("failed to create main.go: %w", err)
	}

	// Run go mod tidy to resolve dependencies
	cmd = exec.Command("go", "mod", "tidy")
	cmd.Dir = workDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy failed: %s", string(output))
	}

	return nil
}

// copyLocalPlugin copies a local plugin to the workspace and modifies it to be a main package
func (pc *PluginCompiler) copyLocalPlugin(workDir, localPath string) error {
	log.Printf("copying local plugin: %s", localPath)

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Copy all files from the source directory directly to workDir
	cmd := exec.Command("cp", "-r", filepath.Join(dir, localPath)+"/.", workDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy local plugin: %s", string(output))
	}

	// Read the plugin's go.mod to get the module path
	pluginModPath, err := pc.getPluginModulePath(workDir)
	if err != nil {
		return fmt.Errorf("failed to determine plugin module path: %w", err)
	}

	// Modify all .go files to change package declaration to main
	err = pc.convertPackageToMain(workDir, pluginModPath)
	if err != nil {
		return fmt.Errorf("failed to convert package to main: %w", err)
	}

	// Enforce main application's dependency versions
	err = pc.enforceMainAppDependencies(workDir, pluginModPath)
	if err != nil {
		return fmt.Errorf("failed to enforce main app dependencies: %w", err)
	}

	// Run go mod tidy to resolve dependencies
	cmd = exec.Command("go", "mod", "tidy")
	cmd.Dir = workDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy failed: %s", string(output))
	}

	return nil
}

// getPluginModulePath reads the go.mod file to extract the module path
func (pc *PluginCompiler) getPluginModulePath(pluginDir string) (string, error) {
	goModPath := filepath.Join(pluginDir, "go.mod")

	// Check if go.mod exists
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		return "", fmt.Errorf("go.mod not found in plugin directory")
	}

	// Read go.mod file
	content, err := os.ReadFile(goModPath)
	if err != nil {
		return "", fmt.Errorf("failed to read go.mod: %w", err)
	}

	// Extract module name from first line
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("empty go.mod file")
	}

	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "module ") {
		return "", fmt.Errorf("invalid go.mod format: missing module declaration")
	}

	modulePath := strings.TrimSpace(strings.TrimPrefix(firstLine, "module "))
	if modulePath == "" {
		return "", fmt.Errorf("empty module path in go.mod")
	}

	return modulePath, nil
}

// convertPackageToMain modifies all .go files to change package declaration to main
func (pc *PluginCompiler) convertPackageToMain(pluginDir, originalPackage string) error {
	// Extract the package name from the module path (last component)
	packageName := filepath.Base(originalPackage)

	// Find all .go files in the plugin directory
	files, err := filepath.Glob(filepath.Join(pluginDir, "*.go"))
	if err != nil {
		return fmt.Errorf("failed to find .go files: %w", err)
	}

	for _, file := range files {
		// Read the file
		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", file, err)
		}

		// Convert the content
		modifiedContent := string(content)

		// Replace package declaration
		oldPackageDecl := fmt.Sprintf("package %s", packageName)
		newPackageDecl := "package main"

		modifiedContent = strings.Replace(modifiedContent, oldPackageDecl, newPackageDecl, 1)

		// Write the modified content back
		if err := os.WriteFile(file, []byte(modifiedContent), 0644); err != nil {
			return fmt.Errorf("failed to write modified file %s: %w", file, err)
		}
	}

	return nil
}

// compilePlugin compiles the plugin source to a .so file
func (pc *PluginCompiler) compilePlugin(workDir, pluginName string) (string, error) {
	log.Printf("compiling plugin: %s", pluginName)

	soPath := filepath.Join(workDir, pluginName+".so")

	// Compile as plugin
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", soPath)
	cmd.Dir = workDir

	// Set environment for compilation
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("compilation failed: %s", string(output))
	}

	// Verify the .so file was created
	if _, err := os.Stat(soPath); err != nil {
		return "", fmt.Errorf("compiled plugin file not found: %w", err)
	}

	log.Printf("successfully compiled plugin to: %s", soPath)
	return soPath, nil
}

// loadCompiledPlugin loads a compiled .so file and initializes the plugin
func (pc *PluginCompiler) loadCompiledPlugin(soPath string, config json.RawMessage) (schemas.Plugin, error) {
	log.Printf("loading compiled plugin: %s", soPath)

	// Load the plugin
	p, err := plugin.Open(soPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open plugin: %w", err)
	}

	// Look for the Init function
	initSymbol, err := p.Lookup("Init")
	if err != nil {
		return nil, fmt.Errorf("init function not found in plugin: %w", err)
	}

	// Cast to the expected function signature
	initFunc, ok := initSymbol.(func(json.RawMessage) (schemas.Plugin, error))
	if !ok {
		return nil, fmt.Errorf("init function has wrong signature")
	}

	// Initialize the plugin with its config
	pluginInstance, err := initFunc(config)
	if err != nil {
		return nil, fmt.Errorf("plugin initialization failed: %w", err)
	}

	return pluginInstance, nil
}

// Cleanup removes all temporary files and directories
func (pc *PluginCompiler) Cleanup() error {
	if pc.tempDir == "" {
		return nil
	}

	log.Printf("cleaning up plugin temp directory: %s", pc.tempDir)

	if err := os.RemoveAll(pc.tempDir); err != nil {
		return fmt.Errorf("failed to cleanup plugin temp directory: %w", err)
	}

	return nil
}

// enforceMainAppDependencies enforces main application's dependency versions on plugins
func (pc *PluginCompiler) enforceMainAppDependencies(workDir, pluginModPath string) error {
	// Get main application's dependencies using hybrid approach
	mainDeps, err := pc.getMainAppDependencies()
	if err != nil {
		log.Printf("warning: could not determine main app dependencies, using plugin's original versions: %v", err)
		return nil // Don't fail, just use plugin's original versions
	}

	// Read plugin's original go.mod to preserve plugin-specific dependencies
	pluginGoModContent, err := os.ReadFile(filepath.Join(workDir, "go.mod"))
	if err != nil {
		return fmt.Errorf("failed to read plugin's go.mod: %w", err)
	}

	pluginDeps := pc.parseGoModRequires(string(pluginGoModContent))

	// Create new go.mod with main app's versions for shared dependencies
	// Keep plugin's versions for plugin-specific dependencies
	var requires []string

	// Add all main app dependencies
	for dep, version := range mainDeps {
		requires = append(requires, fmt.Sprintf("\t%s %s", dep, version))
	}

	// Add plugin-specific dependencies that aren't in main app
	for dep, version := range pluginDeps {
		if _, exists := mainDeps[dep]; !exists {
			requires = append(requires, fmt.Sprintf("\t%s %s", dep, version))
		}
	}

	// Create new go.mod content
	newGoModContent := fmt.Sprintf(`module %s
go 1.21

require (
%s
)
`, pluginModPath, strings.Join(requires, "\n"))

	// Write the new go.mod
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte(newGoModContent), 0644); err != nil {
		return fmt.Errorf("failed to write updated go.mod: %w", err)
	}

	log.Printf("Enforced main app dependency versions on plugin %s", pluginModPath)
	return nil
}

// parseGoModRequires extracts require dependencies from go.mod content
func (pc *PluginCompiler) parseGoModRequires(content string) map[string]string {
	deps := make(map[string]string)
	lines := strings.Split(content, "\n")
	inRequireBlock := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Handle require block start
		if strings.HasPrefix(line, "require (") {
			inRequireBlock = true
			continue
		}

		// Handle require block end
		if inRequireBlock && line == ")" {
			inRequireBlock = false
			continue
		}

		// Handle single-line require
		if strings.HasPrefix(line, "require ") && !strings.Contains(line, "(") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				dep := parts[1]
				version := parts[2]
				deps[dep] = version
			}
			continue
		}

		// Handle require block contents
		if inRequireBlock && line != "" && !strings.HasPrefix(line, "//") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				dep := parts[0]
				version := parts[1]
				// Remove any trailing comments
				if idx := strings.Index(version, "//"); idx != -1 {
					version = strings.TrimSpace(version[:idx])
				}
				deps[dep] = version
			}
		}
	}

	return deps
}

// getMainAppDependencies gets main application dependencies using hybrid approach
func (pc *PluginCompiler) getMainAppDependencies() (map[string]string, error) {
	// Strategy 1: Try local go.mod (development case)
	if deps, err := pc.getLocalGoModDeps(); err == nil {
		log.Printf("Using local go.mod dependencies")
		return deps, nil
	}

	// Strategy 2: Use go list to get runtime dependencies (binary installation case)
	log.Printf("Local go.mod not found, using runtime dependencies from go list")
	return pc.getRuntimeDependencies()
}

// getLocalGoModDeps attempts to read dependencies from local go.mod file
func (pc *PluginCompiler) getLocalGoModDeps() (map[string]string, error) {
	cwd, _ := os.Getwd()

	// Try different possible locations for the transport's go.mod
	possiblePaths := []string{
		filepath.Join(cwd, "go.mod"),             // ./go.mod (current directory)
		filepath.Join(cwd, "..", "go.mod"),       // ../go.mod (parent directory)
		filepath.Join(cwd, "..", "..", "go.mod"), // ../../go.mod (grandparent)
	}

	for _, path := range possiblePaths {
		if content, err := os.ReadFile(path); err == nil {
			log.Printf("Reading local go.mod from: %s", path)
			return pc.parseGoModRequires(string(content)), nil
		}
	}

	return nil, fmt.Errorf("no local go.mod file found")
}

// getRuntimeDependencies gets the dependencies that were used to build the current binary
func (pc *PluginCompiler) getRuntimeDependencies() (map[string]string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Version}}", "all")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime dependencies: %w", err)
	}

	deps := make(map[string]string)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) >= 2 {
			path := parts[0]
			version := parts[1]
			// Skip the main module itself
			if path != "" && version != "" && !strings.Contains(version, "(main)") {
				deps[path] = version
			}
		}
	}

	log.Printf("Retrieved %d runtime dependencies via go list", len(deps))
	return deps, nil
}
