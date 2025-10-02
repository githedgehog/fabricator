# Hedgehog Network Diagram Generator

This document describes the automated diagram generation system for Hedgehog Fabricator network topologies.

## Overview

The `test-diagram` Just target generates comprehensive network diagrams across multiple topology configurations, output formats, and visual styles. It creates an interactive HTML viewer to browse all generated diagrams.

## Quick Start

```bash
# Generate all test topologies (interactive prompt)
just test-diagram

# Auto-confirm without live diagrams
just test-diagram "" "-y"

# Include live diagrams from running VLAB
just test-diagram "/path/to/vlab" "-y"
```

## Generated Topologies

The system generates diagrams for these network topologies:

- **Default**: Standard 2-spine, 2-leaf VLAB configuration
- **3spine**: 3-spine topology with MCLAG and orphan leafs
- **4mclag2orphan**: 4 MCLAG leafs with 2 orphan leafs
- **Mesh**: Mesh topology with inter-leaf connections
- **Live**: Real-time diagrams from running VLAB (optional)

## Output Formats & Styles

### Formats
- **DrawIO**: Editable diagrams for draw.io/diagrams.net
- **Graphviz**: DOT format graphs (converted to SVG if GraphViz installed)
- **Mermaid**: Text-based diagrams (converted to SVG if Mermaid CLI installed)

### DrawIO Styles
- **Default**: Clean, minimal styling
- **Cisco**: Cisco-style network icons
- **Hedgehog**: Hedgehog-branded styling

## Requirements

### Core Requirements
- Just task runner
- Go compiler (for HTML viewer generation)
- Hedgehog Fabricator (`bin/hhfab`)

### Optional Conversion Tools
- **GraphViz**: `sudo apt install graphviz` (for .dot → .svg)
- **Mermaid CLI**: `npm install -g @mermaid-js/mermaid-cli` (for .mermaid → .svg)
- **DrawIO CLI**: For headless .drawio → .svg conversion

### Live Diagram Requirements
- Running VLAB with kubeconfig
- Up-to-date `hhfab` in PATH or vlab directory

## Usage

### Basic Usage

```bash
# Interactive mode with confirmation prompt
just test-diagram

# Skip confirmation prompt
just test-diagram "" "-y"
```

### Live Diagrams

To include live diagrams from a running VLAB:

```bash
# Generate live diagrams from specific VLAB directory
just test-diagram "/home/user/my-vlab" "-y"
```

**Requirements for live diagrams:**
- Running VLAB process (`hhfab vlab up`)
- Valid kubeconfig at `{vlab_workdir}/vlab/kubeconfig`
- System `hhfab` command or local `hhfab` in vlab directory

## Output Structure

```
test-diagram/
├── default/
│   ├── default.drawio
│   ├── cisco.drawio
│   ├── hedgehog.drawio
│   ├── default.dot
│   ├── default.dot.svg      # If GraphViz available
│   ├── default.mermaid
│   └── default.mermaid.svg  # If Mermaid CLI available
├── 3spine/
├── mesh/
├── 4mclag2orphan/
├── live/                    # If vlab_workdir specified
└── diagram-viewer.html      # Interactive HTML viewer
```

## HTML Viewer

The generated `diagram-viewer.html` provides:

- **Tabbed interface** for different formats (DrawIO, Graphviz, Mermaid)
- **Style switching** for DrawIO diagrams (Default/Cisco/Hedgehog)
- **Topology badges** with visual indicators
- **Responsive design** for desktop and mobile
- **Direct SVG rendering** for supported formats

### Opening the Viewer

```bash
# Open in default browser
xdg-open test-diagram/diagram-viewer.html

# Or use specific browser
firefox test-diagram/diagram-viewer.html
```

## Warning

⚠️ **Important**: This script runs `hhfab init` multiple times in the current directory, which will overwrite existing Hedgehog Fabricator configuration and VLAB setup. Only run this in a clean directory or backup your configuration first.

## Examples

### Complete Workflow

```bash
# 1. Clean previous run
just clean-diagram

# 2. Generate all diagrams with live VLAB
just test-diagram "/home/user/production-vlab" "-y"

# 3. Open HTML viewer
firefox test-diagram/diagram-viewer.html
```

### Development Workflow

```bash
# Quick test without live diagrams
just test-diagram "" "-y"

# View results
ls test-diagram/*/
open test-diagram/diagram-viewer.html
```

## Troubleshooting

### No Live Diagrams Generated
- Verify VLAB is running: `ps aux | grep "hhfab vlab up"`
- Check kubeconfig exists: `ls {vlab_workdir}/vlab/kubeconfig`
- Ensure `hhfab` is in PATH or vlab directory

### Missing SVG Files
- Install GraphViz: `sudo apt install graphviz`
- Install Mermaid CLI: `npm install -g @mermaid-js/mermaid-cli`
- Check warnings in command output

### HTML Viewer Shows "0 Topologies"
- Regenerate HTML viewer: `just _create-html`
- Check that SVG files were created successfully
- Verify directory structure matches expected layout

## Files

- `hack/diagrams.just`: Main diagram generation script
- `hack/generate_viewer.go`: HTML viewer generator
- `test-diagram/diagram-viewer.html`: Generated interactive viewer
