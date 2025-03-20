// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"go.githedgehog.com/fabricator/pkg/hhfab/diagram"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Diagram(workDir, format string, styleType diagram.StyleType) error {
	includeDir := filepath.Join(workDir, IncludeDir)

	fabPath := filepath.Join(workDir, "fab.yaml")
	var gatewayNodes []client.Object

	if _, err := os.Stat(fabPath); !os.IsNotExist(err) {
		slog.Debug("Found fab.yaml in workdir", "path", "fab.yaml")
		fabData, err := os.ReadFile(fabPath)
		if err != nil {
			return fmt.Errorf("reading fab.yaml: %w", err)
		}

		fabLoader := apiutil.NewFabLoader()
		fabObjs, err := fabLoader.Load(fabData)
		if err != nil {
			slog.Warn("Error parsing fab.yaml, proceeding without gateway nodes", "error", err)
		} else {
			for _, obj := range fabObjs {
				if obj.GetObjectKind().GroupVersionKind().Kind == "Node" {
					nodeObj := obj.DeepCopyObject()

					specMap := make(map[string]interface{})
					nodeBytes, err := json.Marshal(nodeObj)
					if err != nil {
						continue
					}

					if err := json.Unmarshal(nodeBytes, &specMap); err != nil {
						continue
					}

					if spec, ok := specMap["spec"].(map[string]interface{}); ok {
						if roles, ok := spec["roles"].([]interface{}); ok {
							for _, role := range roles {
								if roleStr, ok := role.(string); ok && roleStr == "gateway" {
									gatewayNodes = append(gatewayNodes, obj)
									slog.Debug("Found gateway node in fab.yaml", "name", obj.GetName())

									break
								}
							}
						}
					}
				}
			}
		}
	}

	files, err := os.ReadDir(includeDir)
	if err != nil {
		return fmt.Errorf("reading include directory: %w", err)
	}

	var yamlFiles []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), YAMLExt) {
			yamlPath := filepath.Join(includeDir, file.Name())
			relPath := filepath.Join(IncludeDir, file.Name())
			yamlFiles = append(yamlFiles, yamlPath)
			slog.Debug("Found YAML file", "path", relPath)
		}
	}

	if len(yamlFiles) == 0 {
		return fmt.Errorf("no YAML files found in include directory") //nolint:goerr113
	}

	var content []byte
	for _, file := range yamlFiles {
		fileContent, err := os.ReadFile(file)
		if err != nil {
			// Extract just the filename for error messages
			fileName := filepath.Base(file)

			return fmt.Errorf("reading file %s: %w", fileName, err)
		}
		if len(content) > 0 {
			content = append(content, []byte("\n---\n")...)
		}
		content = append(content, fileContent...)
	}

	loader := apiutil.NewWiringLoader()
	wiringObjs, err := loader.Load(content)
	if err != nil {
		return fmt.Errorf("loading wiring YAML: %w", err)
	}

	allObjs := append(wiringObjs, gatewayNodes...)

	jsonData, err := json.MarshalIndent(allObjs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}

	format = strings.ToLower(format)
	switch format {
	case "drawio":
		slog.Debug("Generating draw.io diagram", "style", styleType)
		if err := diagram.GenerateDrawio(workDir, jsonData, styleType); err != nil {
			return fmt.Errorf("generating draw.io diagram: %w", err)
		}
		fileName := "vlab-diagram.drawio"
		slog.Info("Generated draw.io diagram", "file", fileName, "style", styleType)
		fmt.Printf("To use this diagram:\n")
		fmt.Printf("1. Open with https://app.diagrams.net/ or the desktop Draw.io application\n")
		fmt.Printf("2. You can edit the diagram and export to PNG, SVG, PDF or other formats\n")
	case "dot":
		slog.Debug("Generating DOT diagram")
		if err := diagram.GenerateDOT(workDir, jsonData); err != nil {
			return fmt.Errorf("generating DOT diagram: %w", err)
		}
		fileName := "vlab-diagram.dot"
		slog.Info("Generated graphviz diagram", "file", fileName)
		fmt.Printf("To render this diagram with Graphviz:\n")
		fmt.Printf("1. Install Graphviz: https://graphviz.org/download/\n")
		fmt.Printf("2. Convert to PNG: dot -Tpng %s -o vlab-diagram.png\n", fileName)
		fmt.Printf("3. Convert to SVG: dot -Tsvg %s -o vlab-diagram.svg\n", fileName)
		fmt.Printf("4. Convert to PDF: dot -Tpdf %s -o vlab-diagram.pdf\n", fileName)
	case "mermaid":
		slog.Debug("Generating Mermaid diagram")
		if err := diagram.GenerateMermaid(workDir, jsonData); err != nil {
			return fmt.Errorf("generating Mermaid diagram: %w", err)
		}
		fileName := "vlab-diagram.mmd"
		slog.Info("Generated Mermaid diagram", "file", fileName)
		fmt.Printf("To render this diagram with Mermaid:\n")
		fmt.Printf("1. Visit https://mermaid.live/ or use a Markdown editor with Mermaid support\n")
		fmt.Printf("2. Copy the contents of %s into the editor\n", fileName)
		fmt.Printf("3. Export to PNG, SVG or other formats as needed\n")
	default:
		return fmt.Errorf("unsupported diagram format: %s", format) //nolint:goerr113
	}

	return nil
}
