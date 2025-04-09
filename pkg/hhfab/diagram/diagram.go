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

func Generate(ctx context.Context, resultDir string, client kclient.Reader, format Format, style StyleType) error {
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

	switch format {
	case FormatDrawio:
		slog.Debug("Generating draw.io diagram", "style", style)
		if err := GenerateDrawio(resultDir, topo, style); err != nil {
			return fmt.Errorf("generating draw.io diagram: %w", err)
		}
		filePath := filepath.Join("result", DrawioFilename)
		slog.Info("Generated draw.io diagram", "file", filePath, "style", style)
		fmt.Printf("To use this diagram:\n")
		fmt.Printf("1. Open with https://app.diagrams.net/ or the desktop Draw.io application\n")
		fmt.Printf("2. You can edit the diagram and export to PNG, SVG, PDF or other formats\n")
	case FormatDot:
		slog.Debug("Generating DOT diagram")
		if err := GenerateDOT(resultDir, topo); err != nil {
			return fmt.Errorf("generating DOT diagram: %w", err)
		}
		filePath := filepath.Join("result", DotFilename)
		slog.Info("Generated graphviz diagram", "file", filePath)
		fmt.Printf("To render this diagram with Graphviz:\n")
		fmt.Printf("1. Install Graphviz: https://graphviz.org/download/\n")
		fmt.Printf("2. Convert to PNG: dot -Tpng %s -o diagram.png\n", filePath)
		fmt.Printf("3. Convert to SVG: dot -Tsvg %s -o diagram.svg\n", filePath)
		fmt.Printf("4. Convert to PDF: dot -Tpdf %s -o diagram.pdf\n", filePath)
	case FormatMermaid:
		slog.Debug("Generating Mermaid diagram")
		if err := GenerateMermaid(resultDir, topo); err != nil {
			return fmt.Errorf("generating Mermaid diagram: %w", err)
		}
		filePath := filepath.Join("result", MermaidFilename)
		slog.Info("Generated Mermaid diagram", "file", filePath)
		fmt.Printf("To render this diagram with Mermaid:\n")
		fmt.Printf("1. Visit https://mermaid.live/ or use a Markdown editor with Mermaid support\n")
		fmt.Printf("2. Copy the contents of %s into the editor\n", filePath)
	default:
		return fmt.Errorf("unsupported diagram format: %s", format) //nolint:goerr113
	}

	return nil
}
