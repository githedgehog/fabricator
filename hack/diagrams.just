# Generate diagrams for multiple environments in different formats and styles
test-diagram y="":
  @mkdir -p test-diagram
  @echo "==============================================="
  @echo "Diagram generation test - various topologies, formats, and styles"
  @echo "==============================================="

  # Check if a VLAB is actually running
  @echo "=== Checking for running VLAB ==="
  @VLAB_PIDS=$(pgrep -f "[h]hfab vlab up" 2>/dev/null || echo ""); \
  if [ -n "$VLAB_PIDS" ] && [ -f "vlab/kubeconfig" ]; then \
    echo "=== Detected running VLAB, generating live diagrams ==="; \
    bin/hhfab diagram --format drawio --style default --live --output test-diagram/live-default.drawio || echo "Failed to generate live DrawIO diagram"; \
    bin/hhfab diagram --format drawio --style cisco --live --output test-diagram/live-cisco.drawio || echo "Failed to generate live DrawIO cisco diagram"; \
    bin/hhfab diagram --format drawio --style hedgehog --live --output test-diagram/live-hedgehog.drawio || echo "Failed to generate live DrawIO hedgehog diagram"; \
    bin/hhfab diagram --format dot --live --output test-diagram/live.dot || echo "Failed to generate live DOT diagram"; \
    if command -v dot >/dev/null 2>&1 && [ -f "test-diagram/live.dot" ]; then \
      dot -Tsvg test-diagram/live-dot.dot -o test-diagram/live.dot.svg; \
    fi; \
    bin/hhfab diagram --format mermaid --live --output test-diagram/live-mermaid.mermaid || echo "Failed to generate live Mermaid diagram"; \
    if [ -f "test-diagram/live.mermaid" ]; then \
      echo '# Live Network Diagram' > test-diagram/live.mermaid.md; \
      echo '```mermaid' >> test-diagram/live.mermaid.md; \
      cat test-diagram/live.mermaid >> test-diagram/live.mermaid.md; \
      echo '```' >> test-diagram/live.mermaid.md; \
    fi; \
  else \
    echo "No running VLAB detected, skipping live diagrams"; \
  fi

  # Skip prompt if -y flag is provided
  @if [ "{{y}}" = "-y" ]; then \
    true; \
  else \
    echo -n "This will generate diagrams from multiple environments. Continue? [y/N] " && read ans && [ "$ans" = "y" -o "$ans" = "Y" ]; \
  fi

  @echo "=== Generating diagrams for default VLAB topology ==="
  bin/hhfab init -f --dev --gw
  bin/hhfab vlab gen

  # Generate all formats and styles for default topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/default-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/default-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/default-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/default.dot
  bin/hhfab diagram --format mermaid --output test-diagram/default.mermaid

  @echo "=== Generating diagrams for variant 3-spine topology ==="
  bin/hhfab vlab gen --spines-count 3 --mclag-leafs-count 2 --orphan-leafs-count 1 --eslag-leaf-groups 2

  # Generate all formats and styles for 3-spine topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/3spine-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/3spine-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/3spine-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/3spine.dot
  bin/hhfab diagram --format mermaid --output test-diagram/3spine.mermaid

  @echo "=== Generating diagrams for 4-mclag-2-orphan topology ==="
  bin/hhfab vlab gen --mclag-leafs-count 4 --orphan-leafs-count 2

  # Generate all formats and styles for 4-mclag-2-orphan topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/4mclag2orphan-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/4mclag2orphan-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/4mclag2orphan-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/4mclag2orphan.dot
  bin/hhfab diagram --format mermaid --output test-diagram/4mclag2orphan.mermaid

  @echo "=== Generating diagrams for mesh topology ==="
  bin/hhfab vlab gen --mesh-links-count 2

  # Generate all formats and styles for 4-mclag-2-orphan topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/mesh-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/mesh-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/mesh-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/mesh.dot
  bin/hhfab diagram --format mermaid --output test-diagram/mesh.mermaid

  @echo "=== Generating diagrams for collapsed core topology ==="
  bin/hhfab init -f --dev --fabric-mode collapsed-core
  bin/hhfab vlab gen

  # Generate all formats and styles for collapsed core topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/collapsed-core-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/collapsed-core-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/collapsed-corehedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/collapsed-core.dot
  bin/hhfab diagram --format mermaid --output test-diagram/collapsed-core.mermaid

  # Convert DRAWIO files to SVG
  @echo "=== Converting DRAWIO files to SVG if GraphViz is installed ==="
  @if command -v drawio >/dev/null 2>&1; then \
    for DRAWIO_FILE in test-diagram/*.drawio; do \
      SVG_FILE="${DRAWIO_FILE}.svg"; \
      echo "Converting $DRAWIO_FILE to $SVG_FILE"; \
      drawio --export --format svg \
             --output "$SVG_FILE" \
             "$DRAWIO_FILE"; \
    done; \
  else \
    echo "Drawio not installed, skipping SVG conversion"; \
  fi

  # Convert DOT files to SVG
  @echo "=== Converting DOT files to SVG if GraphViz is installed ==="
  @if command -v dot >/dev/null 2>&1; then \
    for DOT_FILE in test-diagram/*.dot; do \
      SVG_FILE="${DOT_FILE}.svg"; \
      echo "Converting $DOT_FILE to $SVG_FILE"; \
      dot -Tsvg "$DOT_FILE" -o "$SVG_FILE"; \
    done; \
  else \
    echo "GraphViz dot not installed, skipping SVG conversion"; \
  fi

  # Create markdown files with embedded mermaid diagrams
  @echo "=== Creating Markdown files with embedded Mermaid diagrams ==="
  @for MERMAID_FILE in test-diagram/*.mermaid; do \
    MD_FILE="${MERMAID_FILE%.mermaid}.md"; \
    BASE_NAME=$(basename "$MERMAID_FILE" -mermaid.mermaid); \
    echo "Creating $MD_FILE"; \
    echo "# $BASE_NAME Network Diagram" > "$MD_FILE"; \
    echo '```mermaid' >> "$MD_FILE"; \
    cat "$MERMAID_FILE" >> "$MD_FILE"; \
    echo '```' >> "$MD_FILE"; \
  done

  # Convert Mermaid files to SVG
  @echo "=== Converting Mermaid files to SVG ==="
  @if command -v mmdc >/dev/null 2>&1; then \
    for MERMAID_FILE in test-diagram/*.mermaid; do \
      if [ -f "$MERMAID_FILE" ]; then \
        SVG_FILE="${MERMAID_FILE%.mermaid}.mermaid.svg"; \
        echo "Converting $MERMAID_FILE to $SVG_FILE"; \
        mmdc -i "$MERMAID_FILE" -o "$SVG_FILE" -t default -b white; \
      fi; \
    done; \
  else \
    echo "Mermaid CLI not installed. Install with: npm install -g @mermaid-js/mermaid-cli"; \
  fi

  @echo ""
  @echo "All diagrams generated in test-diagram/ directory"
  @ls -la test-diagram/
  @echo ""
  @echo "Summary of generated files:"
  @echo "- Default VLAB topology: default-*"
  @echo "- 3-spine VLAB topology: 3spine-*"
  @echo "- 4-mclag-2-orphan topology: 4mclag2orphan-*"
  @echo "- Collapsed core topology: collapsed-core-*"
  @echo "- Live diagrams (if VLAB running): live-*"
  @echo ""
  @echo "For each topology, these formats are available:"
  @echo "- DrawIO: *-drawio-{default,cisco,hedgehog}.drawio"
  @echo "- DOT: *-dot.dot (and SVG if GraphViz was installed)"
  @echo "- Mermaid: *-mermaid.mermaid (and embedded in markdown *.md)"

  @just _create-html

  @echo "Quick viewers:"
  @echo "- HTML Multi-format viewer: test-diagram/diagram-viewer.html"
  @echo ""
  @echo "To view the HTML file:"
  @echo "   firefox test-diagram/diagram-viewer.html"
  @echo "   # or"
  @echo "   xdg-open test-diagram/diagram-viewer.html"

# Clean up all generated diagrams
clean-diagram:
  @echo "Cleaning up generated diagrams..."
  @rm -rf test-diagram/
  @echo "Diagrams cleaned"


# Generate HTML viewer for all diagrams
_check-go:
  @command -v go >/dev/null 2>&1 || (echo "Go not installed. Install from https://golang.org" && exit 1)

# Update your existing target
_create-html: _check-go
  @echo "=== Creating HTML viewer with all diagrams ==="
  @go run hack/generate_viewer.go test-diagram
