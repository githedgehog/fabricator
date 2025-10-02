// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

//go:embed templates/*
var templateFiles embed.FS

// Configuration constants
var (
	FilePatterns = struct {
		MermaidSVG  string
		GraphvizSVG string
		DrawIOSVG   string
	}{
		MermaidSVG:  "*.mermaid.svg",
		GraphvizSVG: "*.dot.svg",
		DrawIOSVG:   "*.drawio.svg",
	}

	DiagramStyles = struct {
		Available []string
		Default   string
	}{
		Available: []string{"default", "hedgehog", "cisco"},
		Default:   "default",
	}

	TopologyMappings = struct {
		Normalization map[string]string
		Badges        map[string]string
	}{
		Normalization: map[string]string{},
		Badges: map[string]string{
			"live":          "🔴 Live",
			"default":       "🏠 Default",
			"3spine":        "🌐 Multi-Spine",
			"4mclag2orphan": "🔗 MCLAG",
			"mesh":          "🕸️ Mesh",
			"spine-leaf":    "🌿 Spine-Leaf",
			"gateway":       "🌐 Gateway",
		},
	}

	MaxWorkers = 4
)

// Error types
type DiagramError struct {
	Operation string
	Path      string
	Cause     error
}

func (e *DiagramError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s failed for path '%s': %v", e.Operation, e.Path, e.Cause)
	}

	return fmt.Sprintf("%s failed: %v", e.Operation, e.Cause)
}

type PathSecurityError struct {
	Path   string
	Reason string
}

func (e *PathSecurityError) Error() string {
	return fmt.Sprintf("path security violation: %s (%s)", e.Path, e.Reason)
}

// Data types
type TopologyInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Badge       string `json:"badge"`
	Environment string `json:"environment"`
}

type DiagramFile struct {
	TopologyInfo
	FilePath     string `json:"filePath"`
	ErrorMessage string `json:"error,omitempty"`
	FileExists   bool   `json:"fileExists"`
	Style        string `json:"style,omitempty"`
}

type ProcessedDiagrams struct {
	MermaidFiles  []DiagramFile            `json:"mermaidFiles"`
	GraphvizFiles []DiagramFile            `json:"graphvizFiles"`
	DrawIOFiles   map[string][]DiagramFile `json:"drawioFiles"`
}

type ViewerData struct {
	DiagramCount       int               `json:"diagramCount"`
	FormatCount        int               `json:"formatCount"`
	GeneratedDate      string            `json:"generatedDate"`
	HasDrawIO          bool              `json:"hasDrawIO"`
	HasGraphviz        bool              `json:"hasGraphviz"`
	HasMermaid         bool              `json:"hasMermaid"`
	FirstTab           string            `json:"firstTab"`
	ProcessedData      ProcessedDiagrams `json:"processedData"`
	SupportsDarkMode   bool              `json:"supportsDarkMode"`
	SupportsFullscreen bool              `json:"supportsFullscreen"`
	LazyLoading        bool              `json:"lazyLoading"`
}

type ProcessingOptions struct {
	SearchDirectories []string
	OutputDirectory   string
	RecursiveSearch   bool
	EnableParallel    bool
}

type FileWork struct {
	FilePath string
	FileType string
}

type FileProcessingResult struct {
	DiagramFile DiagramFile
	Error       error
}

// Security functions
func sanitizePath(path string) (string, error) {
	cleanPath := filepath.Clean(path)

	if strings.Contains(cleanPath, "..") {
		return "", &PathSecurityError{Path: path, Reason: "contains directory traversal sequence"}
	}

	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", &PathSecurityError{Path: path, Reason: "failed to resolve absolute path: " + err.Error()}
	}

	return absPath, nil
}

func sanitizePaths(paths []string) ([]string, error) {
	sanitized := make([]string, len(paths))
	for i, path := range paths {
		cleanPath, err := sanitizePath(path)
		if err != nil {
			return nil, err
		}
		sanitized[i] = cleanPath
	}

	return sanitized, nil
}

// File processing functions
func discoverFiles(searchDirectories []string, recursiveSearch bool) (map[string][]string, error) {
	fileMap := make(map[string][]string)

	for _, searchDir := range searchDirectories {
		if recursiveSearch {
			err := filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !info.IsDir() {
					categorizeFile(path, fileMap)
				}

				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("error walking directory %s: %w", searchDir, err)
			}
		} else {
			patterns := []string{FilePatterns.MermaidSVG, FilePatterns.GraphvizSVG, FilePatterns.DrawIOSVG}

			for _, pattern := range patterns {
				patternPath := filepath.Join(searchDir, pattern)
				matches, err := filepath.Glob(patternPath)
				if err != nil {
					continue
				}
				for _, match := range matches {
					categorizeFile(match, fileMap)
				}
			}
		}
	}

	return fileMap, nil
}

func categorizeFile(filePath string, fileMap map[string][]string) {
	fileName := filepath.Base(filePath)

	if matched, _ := filepath.Match(FilePatterns.MermaidSVG, fileName); matched {
		fileMap["mermaid"] = append(fileMap["mermaid"], filePath)
	} else if matched, _ := filepath.Match(FilePatterns.GraphvizSVG, fileName); matched {
		fileMap["graphviz"] = append(fileMap["graphviz"], filePath)
	} else if matched, _ := filepath.Match(FilePatterns.DrawIOSVG, fileName); matched {
		style := determineDrawIOStyle(fileName)
		key := "drawio-" + style
		fileMap[key] = append(fileMap[key], filePath)
	}
}

func determineDrawIOStyle(fileName string) string {
	for _, style := range DiagramStyles.Available {
		if strings.Contains(fileName, style+".drawio.svg") {
			return style
		}
	}

	return DiagramStyles.Default
}

func processFilesParallel(fileMap map[string][]string, outputDirectory string) (*ProcessedDiagrams, error) {
	workChan := make(chan FileWork, 100)
	resultChan := make(chan FileProcessingResult, 100)

	var wg sync.WaitGroup
	for i := 0; i < MaxWorkers; i++ {
		wg.Add(1)
		go worker(workChan, resultChan, &wg, outputDirectory)
	}

	go func() {
		defer close(workChan)
		for fileType, files := range fileMap {
			for _, filePath := range files {
				workChan <- FileWork{
					FilePath: filePath,
					FileType: fileType,
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	return collectResults(resultChan)
}

func worker(workChan <-chan FileWork, resultChan chan<- FileProcessingResult, wg *sync.WaitGroup, outputDirectory string) {
	defer wg.Done()

	for work := range workChan {
		result := processFile(work, outputDirectory)
		resultChan <- result
	}
}

func processFile(work FileWork, outputDirectory string) FileProcessingResult {
	topology := getTopologyInfo(work.FilePath)

	fileInfo, err := os.Stat(work.FilePath)
	diagramFile := DiagramFile{
		TopologyInfo: topology,
		FileExists:   err == nil,
	}

	switch {
	case err != nil:
		diagramFile.ErrorMessage = fmt.Sprintf("Error accessing file: %v", err)
	case fileInfo.IsDir():
		diagramFile.ErrorMessage = "Path is a directory, not a file"
		diagramFile.FileExists = false
	default:
		relPath, err := filepath.Rel(outputDirectory, work.FilePath)
		if err != nil {
			diagramFile.FilePath = filepath.Base(work.FilePath)
		} else {
			diagramFile.FilePath = relPath
		}

		if strings.HasPrefix(work.FileType, "drawio-") {
			diagramFile.Style = strings.TrimPrefix(work.FileType, "drawio-")
		}
	}

	return FileProcessingResult{
		DiagramFile: diagramFile,
		Error:       nil,
	}
}

func collectResults(resultChan <-chan FileProcessingResult) (*ProcessedDiagrams, error) {
	diagrams := &ProcessedDiagrams{
		DrawIOFiles: make(map[string][]DiagramFile),
	}

	for _, style := range DiagramStyles.Available {
		diagrams.DrawIOFiles[style] = []DiagramFile{}
	}

	for result := range resultChan {
		if result.Error != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", result.Error)

			continue
		}

		switch {
		case result.DiagramFile.Style != "":
			style := result.DiagramFile.Style
			diagrams.DrawIOFiles[style] = append(diagrams.DrawIOFiles[style], result.DiagramFile)
		case strings.Contains(result.DiagramFile.FilePath, ".mermaid.svg"):
			diagrams.MermaidFiles = append(diagrams.MermaidFiles, result.DiagramFile)
		case strings.Contains(result.DiagramFile.FilePath, ".dot.svg"):
			diagrams.GraphvizFiles = append(diagrams.GraphvizFiles, result.DiagramFile)
		}
	}

	deduplicateAndSort(diagrams)

	return diagrams, nil
}

func deduplicateAndSort(diagrams *ProcessedDiagrams) {
	diagrams.MermaidFiles = deduplicateDiagrams(diagrams.MermaidFiles)
	diagrams.GraphvizFiles = deduplicateDiagrams(diagrams.GraphvizFiles)

	for style, files := range diagrams.DrawIOFiles {
		diagrams.DrawIOFiles[style] = deduplicateDiagrams(files)
	}
}

func deduplicateDiagrams(diagrams []DiagramFile) []DiagramFile {
	seen := make(map[string]DiagramFile)

	for _, diagram := range diagrams {
		// Use the unique name as the key since it already includes environment
		key := diagram.Name
		if existing, exists := seen[key]; !exists || (existing.ErrorMessage != "" && diagram.ErrorMessage == "") {
			seen[key] = diagram
		}
	}

	result := make([]DiagramFile, 0, len(seen))
	for _, diagram := range seen {
		result = append(result, diagram)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Environment != result[j].Environment {
			return result[i].Environment < result[j].Environment
		}

		return result[i].Name < result[j].Name
	})

	return result
}

func getTopologyInfo(filename string) TopologyInfo {
	dir := filepath.Dir(filename)
	topologyType := filepath.Base(dir)

	// Extract environment from path
	pathParts := strings.Split(filepath.Clean(filename), string(filepath.Separator))
	environment := ""

	// Look for environment patterns (env-*, env-ci-*)
	for _, part := range pathParts {
		if strings.HasPrefix(part, "env-") {
			environment = part

			break
		}
	}

	baseName := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	baseName = strings.TrimSuffix(baseName, ".dot")
	baseName = strings.TrimSuffix(baseName, ".drawio")
	baseName = strings.TrimSuffix(baseName, ".mermaid")

	// Use topology type from directory name
	topologyName := topologyType
	if topologyName == "." || topologyName == "" {
		topologyName = baseName
	}

	if normalized, exists := TopologyMappings.Normalization[topologyName]; exists {
		topologyName = normalized
	}

	// Create unique name by combining environment and topology
	uniqueName := topologyName
	if environment != "" {
		uniqueName = environment + "-" + topologyName
	}

	title := smartTitle(topologyName)
	if environment != "" {
		title = smartTitle(environment) + ": " + title
	}

	badge := "📊 Topology"
	for key, badgeValue := range TopologyMappings.Badges {
		if strings.Contains(topologyName, key) {
			badge = badgeValue

			break
		}
	}

	if environment != "" && strings.Contains(environment, "env-ci-") {
		badge = "🔬 CI Environment"
	} else if environment != "" && strings.HasPrefix(environment, "env-") {
		badge = "🏗️ Lab Environment"
	}

	return TopologyInfo{
		Name:        uniqueName, // Now includes environment prefix
		Title:       title,
		Badge:       badge,
		Environment: environment,
	}
}

func smartTitle(s string) string {
	if strings.HasPrefix(s, "env-") {
		if strings.Contains(s, "env-ci-") {
			return s
		}

		parts := strings.Split(s, "-")
		if len(parts) >= 2 {
			envPart := strings.Join(parts[:2], "-")
			if len(parts) > 2 {
				restParts := parts[2:]
				for i, part := range restParts {
					if len(part) > 0 {
						runes := []rune(part)
						runes[0] = unicode.ToUpper(runes[0])
						restParts[i] = string(runes)
					}
				}

				return envPart + ": " + strings.Join(restParts, " ")
			}

			return envPart
		}
	}

	words := strings.Fields(strings.ReplaceAll(s, "-", " "))
	for i, word := range words {
		if len(word) > 0 {
			runes := []rune(word)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}

	return strings.Join(words, " ")
}

// Viewer generation functions
func buildViewerData(diagrams *ProcessedDiagrams) *ViewerData {
	hasDrawIO := hasDrawIODiagrams(diagrams.DrawIOFiles)
	hasGraphviz := len(diagrams.GraphvizFiles) > 0
	hasMermaid := len(diagrams.MermaidFiles) > 0

	formatCount := 0
	if hasDrawIO {
		formatCount++
	}
	if hasGraphviz {
		formatCount++
	}
	if hasMermaid {
		formatCount++
	}

	firstTab := "drawio"
	if !hasDrawIO && hasGraphviz {
		firstTab = "graphviz"
	} else if !hasDrawIO && !hasGraphviz && hasMermaid {
		firstTab = "mermaid"
	}

	topologyCount := countUniqueTopologies(diagrams)

	return &ViewerData{
		DiagramCount:       topologyCount,
		FormatCount:        formatCount,
		GeneratedDate:      time.Now().Format("2006-01-02"),
		HasDrawIO:          hasDrawIO,
		HasGraphviz:        hasGraphviz,
		HasMermaid:         hasMermaid,
		FirstTab:           firstTab,
		ProcessedData:      *diagrams,
		SupportsDarkMode:   true,
		SupportsFullscreen: false, // Disabled
		LazyLoading:        true,
	}
}

func hasDrawIODiagrams(drawioFiles map[string][]DiagramFile) bool {
	for _, diagrams := range drawioFiles {
		if len(diagrams) > 0 {
			return true
		}
	}

	return false
}

func countUniqueTopologies(diagrams *ProcessedDiagrams) int {
	topologyTypes := make(map[string]bool)

	for _, diagram := range diagrams.MermaidFiles {
		topologyTypes[diagram.Name] = true
	}

	for _, diagram := range diagrams.GraphvizFiles {
		topologyTypes[diagram.Name] = true
	}

	for _, diagrams := range diagrams.DrawIOFiles {
		for _, diagram := range diagrams {
			topologyTypes[diagram.Name] = true
		}
	}

	return len(topologyTypes)
}

func generateHTML(data *ViewerData) (string, error) {
	htmlTemplate, err := templateFiles.ReadFile("templates/viewer.html")
	if err != nil {
		return "", fmt.Errorf("failed to read HTML template: %w", err)
	}

	cssContent, err := templateFiles.ReadFile("templates/styles.css")
	if err != nil {
		return "", fmt.Errorf("failed to read CSS template: %w", err)
	}

	jsContent, err := templateFiles.ReadFile("templates/scripts.js")
	if err != nil {
		return "", fmt.Errorf("failed to read JS template: %w", err)
	}

	tmpl, err := template.New("html").Funcs(template.FuncMap{
		"css": func() template.CSS { return template.CSS(cssContent) }, // #nosec G203
		"js":  func() template.JS { return template.JS(jsContent) },    // #nosec G203
	}).Parse(string(htmlTemplate))
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf strings.Builder
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

func generateViewer(searchDirectories []string, outputDirectory, outputFileName string, recursiveSearch bool) error {
	options := &ProcessingOptions{
		SearchDirectories: searchDirectories,
		OutputDirectory:   outputDirectory,
		RecursiveSearch:   recursiveSearch,
		EnableParallel:    true,
	}

	fileMap, err := discoverFiles(options.SearchDirectories, options.RecursiveSearch)
	if err != nil {
		return &DiagramError{Operation: "file discovery", Cause: err}
	}

	var diagrams *ProcessedDiagrams
	if options.EnableParallel {
		diagrams, err = processFilesParallel(fileMap, options.OutputDirectory)
	} else {
		diagrams, err = processFilesSequential(fileMap, options.OutputDirectory)
	}
	if err != nil {
		return &DiagramError{Operation: "file processing", Cause: err}
	}

	viewerData := buildViewerData(diagrams)

	htmlContent, err := generateHTML(viewerData)
	if err != nil {
		return &DiagramError{Operation: "HTML generation", Cause: err}
	}

	outputPath := filepath.Join(outputDirectory, outputFileName)
	err = os.WriteFile(outputPath, []byte(htmlContent), 0o600)
	if err != nil {
		return &DiagramError{Operation: "file write", Path: outputPath, Cause: err}
	}

	printSummary(outputPath, viewerData)

	return nil
}

func processFilesSequential(fileMap map[string][]string, outputDirectory string) (*ProcessedDiagrams, error) {
	resultChan := make(chan FileProcessingResult, 100)

	go func() {
		defer close(resultChan)
		for fileType, files := range fileMap {
			for _, filePath := range files {
				work := FileWork{FilePath: filePath, FileType: fileType}
				result := processFile(work, outputDirectory)
				resultChan <- result
			}
		}
	}()

	return collectResults(resultChan)
}

func printSummary(outputPath string, data *ViewerData) {
	fmt.Printf("Enhanced HTML viewer created: %s\n", outputPath)
	fmt.Printf("Found %d unique topologies\n", data.DiagramCount)
	fmt.Printf("Found %d Mermaid SVG files\n", len(data.ProcessedData.MermaidFiles))

	drawioCount := 0
	for style, diagrams := range data.ProcessedData.DrawIOFiles {
		count := len(diagrams)
		if count > 0 {
			fmt.Printf("Found %d %s DrawIO SVG files\n", count, style)
		}
		drawioCount += count
	}
	if drawioCount > 0 {
		fmt.Printf("Found %d total DrawIO SVG files\n", drawioCount)
	}
	fmt.Printf("Found %d Graphviz SVG files\n", len(data.ProcessedData.GraphvizFiles))
}

func main() {
	var searchDirectories []string
	var outputDirectory, outputFileName string
	var recursiveSearch bool

	searchDirsFlag := flag.String("dirs", "", "Comma-separated list of directories to search for diagrams")
	flag.StringVar(&outputDirectory, "output-dir", ".", "Directory to write the HTML file")
	flag.StringVar(&outputFileName, "output-file", "diagram-viewer.html", "Name of the HTML file")
	flag.BoolVar(&recursiveSearch, "recursive", true, "Search directories recursively")
	flag.Parse()

	switch {
	case *searchDirsFlag == "" && len(flag.Args()) > 0:
		baseDir := flag.Args()[0]
		searchDirectories = []string{baseDir}
		outputDirectory = baseDir
	case *searchDirsFlag != "":
		searchDirectories = strings.Split(*searchDirsFlag, ",")
		for i, dir := range searchDirectories {
			searchDirectories[i] = strings.TrimSpace(dir)
		}
	default:
		searchDirectories = []string{"."}
	}

	sanitizedSearchDirectories, err := sanitizePaths(searchDirectories)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error sanitizing search paths: %v\n", err)
		os.Exit(1)
	}

	sanitizedOutputDirectory, err := sanitizePath(outputDirectory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error sanitizing output directory: %v\n", err)
		os.Exit(1)
	}

	err = generateViewer(sanitizedSearchDirectories, sanitizedOutputDirectory, outputFileName, recursiveSearch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
