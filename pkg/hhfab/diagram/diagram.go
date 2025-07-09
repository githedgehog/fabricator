// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"

	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type Format string

const (
	FormatDrawio  Format = "drawio"
	FormatDot     Format = "dot"
	FormatMermaid Format = "mermaid"
)

var Formats = []Format{
	FormatDrawio,
	FormatDot,
	FormatMermaid,
}

func getDisplayPath(workDir, filePath string) string {
	displayPath := filePath
	rel, err := filepath.Rel(workDir, filePath)
	if err == nil {
		displayPath = rel
	}

	return displayPath
}

func Generate(ctx context.Context, workDir, resultDir string, client kclient.Reader, format Format, style StyleType, outputPath string) error {
	if !slices.Contains(Formats, format) {
		return fmt.Errorf("unsupported diagram format: %s", format) //nolint:goerr113
	}
	if !slices.Contains(StyleTypes, style) {
		return fmt.Errorf("unsupported diagram style: %s", style) //nolint:goerr113
	}

	topo, err := GetTopologyFor(ctx, client)
	if err != nil {
		return fmt.Errorf("getting topology: %w", err)
	}

	var filePath string

	switch format {
	case FormatDrawio:
		if err := GenerateDrawio(resultDir, topo, style, outputPath); err != nil {
			return fmt.Errorf("generating draw.io diagram: %w", err)
		}
		if outputPath != "" {
			filePath = outputPath
		} else {
			filePath = filepath.Join(resultDir, DrawioFilename)
		}

		displayPath := getDisplayPath(workDir, filePath)

		slog.Info("Generated draw.io diagram", "file", displayPath, "style", style)
		fmt.Printf("To use this diagram:\n")
		fmt.Printf("1. Open with https://app.diagrams.net/ or the desktop Draw.io application\n")
		fmt.Printf("2. You can edit the diagram and export to PNG, SVG, PDF or other formats\n")
	case FormatDot:
		if err := GenerateDOT(resultDir, topo, outputPath); err != nil {
			return fmt.Errorf("generating DOT diagram: %w", err)
		}
		if outputPath != "" {
			filePath = outputPath
		} else {
			filePath = filepath.Join(resultDir, DotFilename)
		}

		displayPath := getDisplayPath(workDir, filePath)

		slog.Info("Generated graphviz diagram", "file", displayPath)
		fmt.Printf("To render this diagram with Graphviz:\n")
		fmt.Printf("1. Install Graphviz: https://graphviz.org/download/\n")
		fmt.Printf("2. Convert to PNG: dot -Tpng %s -o diagram.png\n", displayPath)
		fmt.Printf("3. Convert to SVG: dot -Tsvg %s -o diagram.svg\n", displayPath)
		fmt.Printf("4. Convert to PDF: dot -Tpdf %s -o diagram.pdf\n", displayPath)
	case FormatMermaid:
		if err := GenerateMermaid(resultDir, topo, outputPath); err != nil {
			return fmt.Errorf("generating Mermaid diagram: %w", err)
		}
		if outputPath != "" {
			filePath = outputPath
		} else {
			filePath = filepath.Join(resultDir, MermaidFilename)
		}

		displayPath := getDisplayPath(workDir, filePath)

		slog.Info("Generated Mermaid diagram", "file", displayPath)
		fmt.Printf("To render this diagram with Mermaid:\n")
		fmt.Printf("1. Visit https://mermaid.live/ or use a Markdown editor with Mermaid support\n")
		fmt.Printf("2. Copy the contents of %s into the editor\n", displayPath)
	default:
		return fmt.Errorf("unsupported diagram format: %s", format) //nolint:goerr113
	}

	return nil
}
