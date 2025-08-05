// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// TopologyInfo represents information about a network topology
type TopologyInfo struct {
	Name  string
	Title string
	Badge string
}

// ViewerData holds all data needed for the HTML template
type ViewerData struct {
	DiagramCount  int
	GeneratedDate string
	HasDrawIO     bool
	HasGraphviz   bool
	HasMermaid    bool
	FirstTab      string
	MermaidFiles  []MermaidDiagram
	DrawIOFiles   map[string][]DrawIODiagram
	GraphvizFiles []GraphvizDiagram
}

// MermaidDiagram represents a Mermaid diagram
type MermaidDiagram struct {
	TopologyInfo
	FilePath string
	Error    string
}

// DrawIODiagram represents a DrawIO SVG diagram
type DrawIODiagram struct {
	TopologyInfo
	FilePath string
	Error    string
}

// GraphvizDiagram represents a Graphviz SVG diagram
type GraphvizDiagram struct {
	TopologyInfo
	FilePath string
	Error    string
}

// simpleTitle converts a string to title case (capitalizes first letter of each word)
func simpleTitle(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		if len(word) > 0 {
			runes := []rune(word)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}

	return strings.Join(words, " ")
}

// getTopologyInfo extracts topology information from filename
func getTopologyInfo(filename string) TopologyInfo {
	// Get base name without extension
	baseName := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))

	// Handle .dot.svg files (like mesh.dot.svg -> mesh)
	baseName = strings.TrimSuffix(baseName, ".dot")

	// Handle .drawio.svg files (like mesh-default.drawio.svg -> mesh-default -> mesh)
	baseName = strings.TrimSuffix(baseName, ".drawio")

	// Handle .mermaid.svg files
	baseName = strings.TrimSuffix(baseName, ".mermaid")

	// Handle drawio files (like mesh-default -> mesh)
	styles := []string{"-default", "-cisco", "-hedgehog"}
	for _, style := range styles {
		if strings.HasSuffix(baseName, style) {
			baseName = strings.TrimSuffix(baseName, style)

			break
		}
	}

	// Create title using simple title case
	title := simpleTitle(strings.ReplaceAll(baseName, "-", " "))

	// Determine badge
	badgeMap := map[string]string{
		"live":          "🔴 Live",
		"default":       "🏠 Default",
		"3spine":        "🌐 Multi-Spine",
		"4mclag2orphan": "🔗 MCLAG",
		"mesh":          "🕸️ Mesh",
		"collapsed":     "📦 Collapsed",
	}

	// Get first part of base name for badge lookup
	firstPart := strings.Split(baseName, "-")[0]
	badge, exists := badgeMap[firstPart]
	if !exists {
		badge = "📊 Topology"
	}

	return TopologyInfo{
		Name:  baseName,
		Title: title,
		Badge: badge,
	}
}

// findFiles finds files matching a pattern in the given directory
func findFiles(dir, pattern string) ([]string, error) {
	pattern = filepath.Join(dir, pattern)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to find files with pattern %s: %w", pattern, err)
	}
	sort.Strings(matches)

	return matches, nil
}

// processMermaidFiles processes all mermaid SVG files
func processMermaidFiles(dir string) ([]MermaidDiagram, error) {
	files, err := findFiles(dir, "*.mermaid.svg")
	if err != nil {
		return nil, err
	}

	diagrams := make([]MermaidDiagram, 0, len(files))
	for _, file := range files {
		topology := getTopologyInfo(file)

		// Check if file exists and is readable
		_, err := os.Stat(file)
		diagram := MermaidDiagram{TopologyInfo: topology}

		if err != nil {
			diagram.Error = fmt.Sprintf("Error accessing file: %v", err)
		} else {
			// Store relative path to the file
			diagram.FilePath = filepath.Base(file)
		}

		diagrams = append(diagrams, diagram)
	}

	return diagrams, nil
}

// processDrawIOFiles processes all DrawIO SVG files grouped by style
func processDrawIOFiles(dir string) (map[string][]DrawIODiagram, error) {
	styles := []string{"default", "hedgehog", "cisco"}
	result := make(map[string][]DrawIODiagram)

	for _, style := range styles {
		pattern := fmt.Sprintf("*%s.drawio.svg", style)
		files, err := findFiles(dir, pattern)
		if err != nil {
			return nil, err
		}

		diagrams := make([]DrawIODiagram, 0, len(files))
		for _, file := range files {
			topology := getTopologyInfo(file)

			// Check if file exists and is readable
			_, err := os.Stat(file)
			diagram := DrawIODiagram{TopologyInfo: topology}

			if err != nil {
				diagram.Error = fmt.Sprintf("Error accessing file: %v", err)
			} else {
				// Store relative path to the file
				diagram.FilePath = filepath.Base(file)
			}

			diagrams = append(diagrams, diagram)
		}

		result[style] = diagrams
	}

	return result, nil
}

// processGraphvizFiles processes all Graphviz SVG files
func processGraphvizFiles(dir string) ([]GraphvizDiagram, error) {
	files, err := findFiles(dir, "*.dot.svg")
	if err != nil {
		return nil, err
	}

	diagrams := make([]GraphvizDiagram, 0, len(files))
	for _, file := range files {
		topology := getTopologyInfo(file)

		// Check if file exists and is readable
		_, err := os.Stat(file)
		diagram := GraphvizDiagram{TopologyInfo: topology}

		if err != nil {
			diagram.Error = fmt.Sprintf("Error accessing file: %v", err)
		} else {
			// Store relative path to the file
			diagram.FilePath = filepath.Base(file)
		}

		diagrams = append(diagrams, diagram)
	}

	return diagrams, nil
}

// generateHTMLViewer generates the HTML viewer
func generateHTMLViewer(diagramDir string) error {
	// Ensure we're working with absolute paths
	absDir, err := filepath.Abs(diagramDir)
	if err != nil {
		return fmt.Errorf("error getting absolute path: %w", err) //nolint:goerr113
	}

	// Process all file types
	mermaidFiles, err := processMermaidFiles(absDir)
	if err != nil {
		return fmt.Errorf("error processing mermaid files: %w", err) //nolint:goerr113
	}

	drawioFiles, err := processDrawIOFiles(absDir)
	if err != nil {
		return fmt.Errorf("error processing drawio files: %w", err) //nolint:goerr113
	}

	graphvizFiles, err := processGraphvizFiles(absDir)
	if err != nil {
		return fmt.Errorf("error processing graphviz files: %w", err) //nolint:goerr113
	}

	// Determine what formats are available
	hasDrawIO := len(drawioFiles["default"]) > 0 || len(drawioFiles["hedgehog"]) > 0 || len(drawioFiles["cisco"]) > 0
	hasGraphviz := len(graphvizFiles) > 0
	hasMermaid := len(mermaidFiles) > 0

	// Determine first active tab
	firstTab := "mermaid"
	if hasDrawIO {
		firstTab = "drawio"
	} else if hasGraphviz {
		firstTab = "graphviz"
	}

	// Prepare data for template
	data := ViewerData{
		DiagramCount:  len(mermaidFiles),
		GeneratedDate: time.Now().Format("2006-01-02"),
		HasDrawIO:     hasDrawIO,
		HasGraphviz:   hasGraphviz,
		HasMermaid:    hasMermaid,
		FirstTab:      firstTab,
		MermaidFiles:  mermaidFiles,
		DrawIOFiles:   drawioFiles,
		GraphvizFiles: graphvizFiles,
	}

	// Generate HTML content
	htmlContent, err := generateHTML(data)
	if err != nil {
		return fmt.Errorf("error generating HTML: %w", err) //nolint:goerr113
	}

	// Write HTML file with secure permissions
	outputFile := filepath.Join(absDir, "diagram-viewer.html")
	err = os.WriteFile(outputFile, []byte(htmlContent), 0600)
	if err != nil {
		return fmt.Errorf("error writing HTML file: %w", err) //nolint:goerr113
	}

	fmt.Printf("HTML viewer created: %s\n", outputFile)

	// Print debug information
	fmt.Printf("Found %d Mermaid SVG files\n", len(mermaidFiles))

	drawioCount := 0
	for _, diagrams := range drawioFiles {
		drawioCount += len(diagrams)
	}
	fmt.Printf("Found %d DrawIO SVG files\n", drawioCount)
	fmt.Printf("Found %d Graphviz SVG files\n", len(graphvizFiles))

	return nil
}

// generateHTML generates the complete HTML content using templates
func generateHTML(data ViewerData) (string, error) {
	tmpl := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>🦔 Hedgehog Network Diagrams</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 0; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); min-height: 100vh; }
        .header { background: rgba(255,255,255,0.1); backdrop-filter: blur(10px); border-bottom: 1px solid rgba(255,255,255,0.2); padding: 20px 0; margin-bottom: 30px; }
        .container { max-width: 1400px; margin: 0 auto; padding: 0 20px; }
        h1 { color: white; text-align: center; margin: 0; font-size: 2.5em; font-weight: 300; text-shadow: 0 2px 4px rgba(0,0,0,0.3); }
        .subtitle { color: rgba(255,255,255,0.8); text-align: center; margin: 10px 0 0 0; font-size: 1.1em; }
        .tabs { display: flex; gap: 10px; margin: 20px 0; justify-content: center; flex-wrap: wrap; }
        .tab { background: rgba(255,255,255,0.2); color: white; border: none; padding: 10px 20px; border-radius: 25px; cursor: pointer; transition: all 0.3s ease; }
        .tab.active { background: white; color: #667eea; }
        .tab:hover { background: rgba(255,255,255,0.3); }
        .style-selector { display: flex; gap: 5px; margin: 15px 0; justify-content: center; flex-wrap: wrap; }
        .style-btn { background: rgba(102, 126, 234, 0.1); color: #667eea; border: 2px solid rgba(102, 126, 234, 0.3); padding: 5px 15px; border-radius: 15px; cursor: pointer; transition: all 0.2s ease; font-size: 0.9em; font-weight: 500; }
        .style-btn.active { background: #667eea; color: white; border-color: #667eea; }
        .style-btn:hover { background: rgba(102, 126, 234, 0.2); }
        h2 { color: #2c3e50; margin: 40px 0 20px 0; font-size: 1.8em; font-weight: 600; }
        .diagram-container { background: white; border-radius: 12px; padding: 30px; margin: 30px 0; box-shadow: 0 8px 32px rgba(0,0,0,0.1); transition: transform 0.2s ease, box-shadow 0.2s ease; }
        .diagram-container:hover { transform: translateY(-2px); box-shadow: 0 12px 48px rgba(0,0,0,0.15); }
        .diagram { text-align: center; margin: 20px 0; }
        .svg-container { position: relative; }
        .svg-diagram { max-width: 100%; height: auto; border-radius: 8px; box-shadow: 0 4px 8px rgba(0,0,0,0.1); display: block; margin: 0 auto; }
        .svg-tooltip { position: absolute; background: rgba(0,0,0,0.8); color: white; padding: 8px 12px; border-radius: 4px; font-size: 12px; pointer-events: none; opacity: 0; transition: opacity 0.2s; z-index: 1000; }
        .svg-tooltip.visible { opacity: 1; }
        .toc { background: rgba(255,255,255,0.95); border-radius: 12px; padding: 25px; margin-bottom: 30px; box-shadow: 0 8px 32px rgba(0,0,0,0.1); backdrop-filter: blur(10px); }
        .toc h3 { color: #2c3e50; margin: 0 0 20px 0; font-size: 1.4em; display: flex; align-items: center; gap: 10px; }
        .toc ul { list-style: none; padding: 0; margin: 0; display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 10px; }
        .toc li { margin: 0; }
        .toc a { color: #667eea; text-decoration: none; padding: 8px 12px; border-radius: 6px; display: block; transition: all 0.2s ease; border-left: 3px solid transparent; }
        .toc a:hover { background: rgba(102, 126, 234, 0.1); border-left-color: #667eea; }
        .topology-badge { display: inline-block; background: linear-gradient(45deg, #667eea, #764ba2); color: white; padding: 4px 12px; border-radius: 20px; font-size: 0.85em; font-weight: 500; margin-left: 10px; }
        .tab-content { display: none; }
        .tab-content.active { display: block; }
        .style-content { display: none; }
        .style-content.active { display: block; }
        .stats { display: flex; justify-content: center; gap: 30px; margin: 20px 0; flex-wrap: wrap; }
        .stat { background: rgba(255,255,255,0.2); padding: 15px 25px; border-radius: 10px; text-align: center; color: white; }
        .stat-number { font-size: 2em; font-weight: bold; display: block; }
        .stat-label { font-size: 0.9em; opacity: 0.9; }
        .no-content { padding: 20px; text-align: center; color: #666; font-style: italic; }
        @media (max-width: 768px) { .container { padding: 0 15px; } .toc ul { grid-template-columns: 1fr; } }
    </style>
</head>
<body>
    <div class="header">
        <div class="container">
            <h1>🦔 Hedgehog Network Diagrams</h1>
            <p class="subtitle">Network Topology Visualizations</p>
            <div class="stats">
                <div class="stat"><span class="stat-number">{{.DiagramCount}}</span><span class="stat-label">Topologies</span></div>
                <div class="stat"><span class="stat-number">3</span><span class="stat-label">Formats</span></div>
                <div class="stat"><span class="stat-number">{{.GeneratedDate}}</span><span class="stat-label">Generated</span></div>
            </div>
            <div class="tabs">
                {{if .HasDrawIO}}<button class="tab{{if eq .FirstTab "drawio"}} active{{end}}" onclick="showTab('drawio')">🎨 DrawIO</button>{{end}}
                {{if .HasGraphviz}}<button class="tab{{if eq .FirstTab "graphviz"}} active{{end}}" onclick="showTab('graphviz')">🔗 Graphviz</button>{{end}}
                {{if .HasMermaid}}<button class="tab{{if eq .FirstTab "mermaid"}} active{{end}}" onclick="showTab('mermaid')">📊 Mermaid</button>{{end}}
            </div>
        </div>
    </div>
    <div class="container">
        {{if .HasDrawIO}}
        <div id="drawio-content" class="tab-content{{if eq .FirstTab "drawio"}} active{{end}}">
            <div class="toc">
                <h3>🎨 DrawIO Network Diagrams</h3>
                <div class="style-selector">
                    <button class="style-btn active" onclick="showDrawIOStyle('default')">Default</button>
                    <button class="style-btn" onclick="showDrawIOStyle('hedgehog')">Hedgehog</button>
                    <button class="style-btn" onclick="showDrawIOStyle('cisco')">Cisco</button>
                </div>
            </div>
            {{range $style, $diagrams := .DrawIOFiles}}
            <div id="drawio-style-{{$style}}" class="style-content{{if eq $style "default"}} active{{end}}">
                {{if $diagrams}}
                    {{range $diagrams}}
                    <div class="diagram-container">
                        <h2>{{.Title}}</h2>
                        <div class="diagram">
                            {{if .Error}}
                            <div class="no-content">{{.Error}}</div>
                            {{else}}
                            <div class="svg-container">
                                <object data="{{.FilePath}}" type="image/svg+xml" class="svg-diagram">
                                    <img src="{{.FilePath}}" alt="{{.Title}} Diagram" />
                                </object>
                                <div class="svg-tooltip" id="tooltip-{{.Name}}"></div>
                            </div>
                            {{end}}
                        </div>
                    </div>
                    {{end}}
                {{else}}
                <div class="diagram-container">
                    <div class="no-content">No {{$style}} style diagrams found. Looking for files matching pattern: *{{$style}}.drawio.svg</div>
                </div>
                {{end}}
            </div>
            {{end}}
        </div>
        {{end}}

        {{if .HasGraphviz}}
        <div id="graphviz-content" class="tab-content{{if eq .FirstTab "graphviz"}} active{{end}}">
            <div class="toc">
                <h3>🔗 Graphviz Network Diagrams</h3>
                <ul>
                    {{range .GraphvizFiles}}
                    <li><a href="#graphviz-{{.Name}}">{{.Title}}<span class="topology-badge">{{.Badge}}</span></a></li>
                    {{end}}
                </ul>
            </div>
            {{if .GraphvizFiles}}
                {{range .GraphvizFiles}}
                <div class="diagram-container">
                    <h2 id="graphviz-{{.Name}}">{{.Title}}</h2>
                    <div class="diagram">
                        {{if .Error}}
                        <div class="no-content">{{.Error}}</div>
                        {{else}}
                        <div class="svg-container">
                            <object data="{{.FilePath}}" type="image/svg+xml" class="svg-diagram">
                                <img src="{{.FilePath}}" alt="{{.Title}} Diagram" />
                            </object>
                            <div class="svg-tooltip" id="tooltip-{{.Name}}"></div>
                        </div>
                        {{end}}
                    </div>
                </div>
                {{end}}
            {{else}}
            <div class="diagram-container">
                <div class="no-content">No Graphviz diagrams found. Looking for files matching pattern: *.dot.svg</div>
            </div>
            {{end}}
        </div>
        {{end}}

        {{if .HasMermaid}}
        <div id="mermaid-content" class="tab-content{{if eq .FirstTab "mermaid"}} active{{end}}">
            <div class="toc">
                <h3>📋 Mermaid Network Diagrams</h3>
                <ul>
                    {{range .MermaidFiles}}
                    <li><a href="#mermaid-{{.Name}}">{{.Title}}<span class="topology-badge">{{.Badge}}</span></a></li>
                    {{end}}
                </ul>
            </div>
            {{if .MermaidFiles}}
                {{range .MermaidFiles}}
                <div class="diagram-container">
                    <h2 id="mermaid-{{.Name}}">{{.Title}}</h2>
                    <div class="diagram">
                        {{if .Error}}
                        <div class="no-content">{{.Error}}</div>
                        {{else}}
                        <div class="svg-container">
                            <object data="{{.FilePath}}" type="image/svg+xml" class="svg-diagram">
                                <img src="{{.FilePath}}" alt="{{.Title}} Diagram" />
                            </object>
                            <div class="svg-tooltip" id="tooltip-{{.Name}}"></div>
                        </div>
                        {{end}}
                    </div>
                </div>
                {{end}}
            {{else}}
            <div class="diagram-container">
                <div class="no-content">No Mermaid diagrams found. Looking for files matching pattern: *.mermaid.svg</div>
            </div>
            {{end}}
        </div>
        {{end}}
    </div>
    <script>
        function showTab(tabName) {
            // Update tab appearance
            document.querySelectorAll(".tab-content").forEach(content => content.classList.remove("active"));
            document.querySelectorAll(".tab").forEach(tab => tab.classList.remove("active"));
            document.getElementById(tabName + "-content").classList.add("active");
            event.target.classList.add("active");
        }

        function showDrawIOStyle(style) {
            document.querySelectorAll(".style-content").forEach(content => content.classList.remove("active"));
            document.querySelectorAll(".style-btn").forEach(btn => btn.classList.remove("active"));
            document.getElementById("drawio-style-" + style).classList.add("active");
            event.target.classList.add("active");
        }

        // SVG interaction helpers
        function showSVGTooltip(diagramName, content, x, y) {
            const tooltip = document.getElementById('tooltip-' + diagramName);
            if (tooltip) {
                tooltip.textContent = content;
                tooltip.style.left = x + 'px';
                tooltip.style.top = y + 'px';
                tooltip.classList.add('visible');
            }
        }

        function hideSVGTooltip(diagramName) {
            const tooltip = document.getElementById('tooltip-' + diagramName);
            if (tooltip) {
                tooltip.classList.remove('visible');
            }
        }

        // Global functions that SVGs can call
        window.showTooltip = showSVGTooltip;
        window.hideTooltip = hideSVGTooltip;
    </script>
</body>
</html>`

	// Parse and execute template
	t, err := template.New("html").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML template: %w", err)
	}

	var buf strings.Builder
	err = t.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTML template: %w", err)
	}

	return buf.String(), nil
}

func main() {
	// Get diagram directory from command line argument or use default
	diagramDir := "test-diagram"
	if len(os.Args) > 1 {
		diagramDir = os.Args[1]
	}

	// Generate the HTML viewer
	err := generateHTMLViewer(diagramDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
