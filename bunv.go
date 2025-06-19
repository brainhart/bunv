package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var withPackages []string

func generateDependencyHash(packages []string) string {
	allDeps := append([]string{"@types/node:latest"}, packages...)
	sort.Strings(allDeps)

	hasher := sha256.New()
	for _, dep := range allDeps {
		hasher.Write([]byte(dep))
	}

	return fmt.Sprintf("%x", hasher.Sum(nil))[:16]
}

const packageJSONTemplate = `{
  "name": "bunv-temp",
  "version": "1.0.0",
  "dependencies": {
		%s
  }
}`

func getCacheDir(hash string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "bunv-cache", hash)
	}
	return filepath.Join(homeDir, ".bunv", "cache", hash)
}

var rootCmd = &cobra.Command{
	Use:   "bunv",
	Short: "Run TypeScript files with Bun and temporary dependencies",
}

type Dependencies map[string]string

func (d Dependencies) HashString() string {
	depList := []string{}
	for k, v := range d {
		depList = append(depList, fmt.Sprintf("%s@%s", k, v))
	}
	s := strings.Join(depList, ",")
	hasher := sha256.New()
	hasher.Write([]byte(s))
	return fmt.Sprintf("%x", hasher.Sum(nil))[:16]
}

func getDependencies(scriptFile string) Dependencies {
	// Extract dependencies from script header
	headerDeps, _ := extractDependenciesFromHeader(scriptFile)
	mergedDeps := map[string]string{"@types/node": "latest"}
	for _, pkg := range withPackages {
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			depName := pkg
			depVer := "latest"
			if at := strings.LastIndex(pkg, "@"); at > 0 {
				depName = pkg[:at]
				depVer = pkg[at+1:]
			}
			mergedDeps[depName] = depVer
		}
	}
	for k, v := range headerDeps {
		mergedDeps[k] = v
	}
	return Dependencies(mergedDeps)
}

var runCmd = &cobra.Command{
	Use:   "run [script.ts] [-- script-args...]",
	Short: "Run a TypeScript file with optional dependencies",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		scriptFile := args[0]
		scriptArgs := args[1:]

		if _, err := os.Stat(scriptFile); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: File %s does not exist\n", scriptFile)
			os.Exit(1)
		}

		deps := getDependencies(scriptFile)
		depHash := deps.HashString()
		cacheDir := getCacheDir(depHash)
		needInstall := false

		if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
			needInstall = true
			if err := os.MkdirAll(cacheDir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating cache directory: %v\n", err)
				os.Exit(1)
			}
		}

		if needInstall {
			depEntries := []string{}
			for k, v := range deps {
				depEntries = append(depEntries, fmt.Sprintf("\"%s\": \"%s\"", k, v))
			}
			sort.Strings(depEntries)
			packageJSON := fmt.Sprintf(packageJSONTemplate, strings.Join(depEntries, ",\n    "))

			packageJSONPath := filepath.Join(cacheDir, "package.json")
			var prettyJSON bytes.Buffer
			if err := json.Indent(&prettyJSON, []byte(packageJSON), "", "  "); err != nil {
				fmt.Fprintf(os.Stderr, "Error formatting package.json as JSON: %v\n", err)
				os.Exit(1)
			}
			if err := os.WriteFile(packageJSONPath, prettyJSON.Bytes(), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing package.json: %v\n", err)
				os.Exit(1)
			}
		}

		nodeModulesPath := filepath.Join(cacheDir, "node_modules")
		if _, err := os.Stat(nodeModulesPath); os.IsNotExist(err) && len(deps) > 1 {
			fmt.Fprintf(os.Stderr, "Installing packages...\n")
			installCmd := exec.Command("bun", "install")
			installCmd.Dir = cacheDir
			installCmd.Stdout = os.Stderr
			installCmd.Stderr = os.Stderr
			if err := installCmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Error installing packages: %v\n", err)
				os.Exit(1)
			}
		}

		absScriptPath, err := filepath.Abs(scriptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting absolute path: %v\n", err)
			os.Exit(1)
		}

		scriptBase := filepath.Base(scriptFile)
		hardlinkScriptPath := filepath.Join(cacheDir, scriptBase)
		os.Remove(hardlinkScriptPath)
		if err := os.Link(absScriptPath, hardlinkScriptPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating hardlink to script file: %v\n", err)
			os.Exit(1)
		}

		bunArgs := append([]string{"run", hardlinkScriptPath}, scriptArgs...)
		bunCmd := exec.Command("bun", bunArgs...)

		// Set NODE_PATH to the cacheDir, plus any existing NODE_PATH
		env := os.Environ()
		nodePathSet := false
		for i, v := range env {
			if strings.HasPrefix(v, "NODE_PATH=") {
				env[i] = fmt.Sprintf("NODE_PATH=%s%c%s", cacheDir, os.PathListSeparator, v[len("NODE_PATH="):])
				nodePathSet = true
				break
			}
		}
		if !nodePathSet {
			env = append(env, fmt.Sprintf("NODE_PATH=%s", cacheDir))
		}
		bunCmd.Env = env

		bunCmd.Stdout = os.Stdout
		bunCmd.Stderr = os.Stderr
		bunCmd.Stdin = os.Stdin

		if err := bunCmd.Run(); err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
					os.Exit(status.ExitStatus())
				}
			}
			os.Exit(1)
		}
	},
}

var addCmd = &cobra.Command{
	Use:   "add --script <script.ts> <dep[@version]>...",
	Short: "Add dependencies to a TypeScript script's inline metadata",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		scriptFile, _ := cmd.Flags().GetString("script")
		if scriptFile == "" {
			fmt.Fprintf(os.Stderr, "Error: --script flag is required\n")
			os.Exit(1)
		}
		if _, err := os.Stat(scriptFile); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: File %s does not exist\n", scriptFile)
			os.Exit(1)
		}

		// Read the whole file
		origBytes, err := os.ReadFile(scriptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading script file: %v\n", err)
			os.Exit(1)
		}
		orig := string(origBytes)

		// Regex to find the metadata block
		blockRe := regexp.MustCompile(`(?ms)^// /// script\n(?P<block>(?:^//.*\n)*?)^// ///\n?`)
		matches := blockRe.FindStringSubmatchIndex(orig)

		var before, after, blockContent string
		if matches != nil {
			before = orig[:matches[0]]
			after = orig[matches[1]:]
			blockContent = orig[matches[2]:matches[3]]
		} else {
			before = ""
			after = orig
			blockContent = ""
		}

		// Extract JSON from blockContent
		jsonLines := []string{}
		for _, line := range strings.Split(blockContent, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "//") {
				jsonLines = append(jsonLines, strings.TrimSpace(strings.TrimPrefix(line, "//")))
			}
		}
		jsonContent := strings.Join(jsonLines, "\n")
		var header map[string]interface{}
		if jsonContent != "" {
			_ = json.Unmarshal([]byte(jsonContent), &header)
		}
		if header == nil {
			header = map[string]interface{}{}
		}
		deps, _ := header["dependencies"].(map[string]interface{})
		if deps == nil {
			deps = map[string]interface{}{}
		}

		// Parse new dependencies from args
		for _, depArg := range args {
			depName := depArg
			depVer := "latest"
			if at := strings.LastIndex(depArg, "@"); at > 0 {
				depName = depArg[:at]
				depVer = depArg[at+1:]
			}
			deps[depName] = depVer
		}
		header["dependencies"] = deps

		// Re-serialize the block
		blockJSON, err := json.MarshalIndent(header, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error serializing metadata: %v\n", err)
			os.Exit(1)
		}
		blockLines := []string{"// /// script"}
		for _, line := range strings.Split(string(blockJSON), "\n") {
			blockLines = append(blockLines, "// "+line)
		}
		blockLines = append(blockLines, "// ///")
		newBlock := strings.Join(blockLines, "\n") + "\n"

		// Reconstruct the file
		var newContent string
		if matches != nil {
			newContent = before + newBlock + after
		} else {
			// Insert at the top, with a blank line after block if file is not empty
			if strings.TrimSpace(after) != "" {
				newContent = newBlock + "\n" + after
			} else {
				newContent = newBlock
			}
		}

		if err := os.WriteFile(scriptFile, []byte(newContent), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing updated script: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Updated dependencies in %s\n", scriptFile)
	},
}

func init() {
	runCmd.Flags().StringSliceVar(&withPackages, "with", []string{}, "Packages to install temporarily")
	rootCmd.AddCommand(runCmd)
	addCmd.Flags().String("script", "", "Script file to update")
	addCmd.MarkFlagRequired("script")
	rootCmd.AddCommand(addCmd)
}

// extractDependenciesFromHeader scans for a block starting with '// /// script', ending with '// ///', and parses the JSON content in between.
func extractDependenciesFromHeader(scriptPath string) (map[string]string, error) {
	f, err := os.Open(scriptPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	maxLines := 30
	lineNum := 0
	inBlock := false
	var jsonLines []string
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++
		if lineNum > maxLines {
			break
		}
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "// /// script" {
				inBlock = true
			}
			continue
		}
		if trimmed == "// ///" {
			break
		}
		if strings.HasPrefix(trimmed, "//") {
			jsonLines = append(jsonLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "//")))
		}
	}
	if len(jsonLines) == 0 {
		return nil, nil // No block found
	}
	jsonContent := strings.Join(jsonLines, "\n")
	var header map[string]interface{}
	if err := json.Unmarshal([]byte(jsonContent), &header); err != nil {
		return nil, nil // Invalid JSON
	}
	deps := map[string]string{}
	if depObj, ok := header["dependencies"].(map[string]interface{}); ok {
		for k, v := range depObj {
			if s, ok := v.(string); ok {
				deps[k] = s
			}
		}
	}
	return deps, nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
