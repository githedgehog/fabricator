# Generate diagrams for multiple environments in different formats and styles

test-diagram vlab_workdir="" y="": _check-hhfab
  @if [ -n "{{vlab_workdir}}" ]; then mkdir -p test-diagram/live; fi
  @mkdir -p test-diagram/default test-diagram/3spine test-diagram/mesh test-diagram/4mclag2orphan
  @echo "==============================================="
  @echo "Diagram generation test - various topologies, formats, and styles"
  @if [ -n "{{vlab_workdir}}" ]; then echo "Live diagrams from vlab workdir: {{vlab_workdir}}"; fi
  @echo "==============================================="

  # Check if a VLAB is actually running
  @if [ -n "{{vlab_workdir}}" ]; then \
    echo "=== Checking for running VLAB ==="; \
    VLAB_PIDS=$(pgrep -f "[h]hfab vlab up" 2>/dev/null || echo ""); \
    if [ -n "$VLAB_PIDS" ] && [ -f "{{vlab_workdir}}/vlab/kubeconfig" ]; then \
      echo "=== Detected running VLAB in {{vlab_workdir}}, generating live diagrams ==="; \
      HHFAB_CMD="hhfab"; \
      if [ -x "{{vlab_workdir}}/hhfab" ]; then \
        echo "Using local hhfab from {{vlab_workdir}}"; \
        HHFAB_CMD="./hhfab"; \
      else \
        echo "Using system hhfab (ensure it's up to date for live diagram support)"; \
      fi; \
      LIVE_OUTPUT_DIR="$(pwd)/test-diagram/live"; \
      mkdir -p "$LIVE_OUTPUT_DIR"; \
      cd {{vlab_workdir}} && $HHFAB_CMD diagram --format drawio --style default --live --output "$LIVE_OUTPUT_DIR/default.drawio" || echo "Failed to generate live DrawIO diagram"; \
      cd {{vlab_workdir}} && $HHFAB_CMD diagram --format drawio --style cisco --live --output "$LIVE_OUTPUT_DIR/cisco.drawio" || echo "Failed to generate live DrawIO cisco diagram"; \
      cd {{vlab_workdir}} && $HHFAB_CMD diagram --format drawio --style hedgehog --live --output "$LIVE_OUTPUT_DIR/hedgehog.drawio" || echo "Failed to generate live DrawIO hedgehog diagram"; \
      cd {{vlab_workdir}} && $HHFAB_CMD diagram --format dot --live --output "$LIVE_OUTPUT_DIR/live.dot" || echo "Failed to generate live DOT diagram"; \
      cd {{vlab_workdir}} && $HHFAB_CMD diagram --format mermaid --live --output "$LIVE_OUTPUT_DIR/live.mermaid" || echo "Failed to generate live Mermaid diagram"; \
    elif [ -n "$VLAB_PIDS" ]; then \
      echo "Found running VLAB process but no kubeconfig in {{vlab_workdir}}, skipping live diagrams"; \
    elif [ -f "{{vlab_workdir}}/vlab/kubeconfig" ]; then \
      echo "Found kubeconfig in {{vlab_workdir}} but no running VLAB process, skipping live diagrams"; \
    else \
      echo "No running VLAB detected, skipping live diagrams"; \
    fi; \
  else \
    echo "=== No vlab_workdir specified, skipping live diagrams ==="; \
  fi

  # Skip prompt if -y flag is provided
  @if [ "{{y}}" = "-y" ]; then \
    true; \
  else \
    echo "WARNING: This will run 'hhfab init' multiple times in the current directory,"; \
    echo "which will overwrite any existing hhfab configuration and VLAB setup."; \
    echo -n "Continue? [y/N] " && read ans && [ "$ans" = "y" -o "$ans" = "Y" ]; \
  fi

  @echo "=== Generating diagrams for default VLAB topology ==="
  bin/hhfab init -f --dev --gw
  bin/hhfab vlab gen

  # Generate all formats and styles for default topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/default/default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/default/cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/default/hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/default/default.dot
  bin/hhfab diagram --format mermaid --output test-diagram/default/default.mermaid

  @echo "=== Generating diagrams for variant 3-spine topology ==="
  bin/hhfab vlab gen --spines-count 3 --mclag-leafs-count 2 --orphan-leafs-count 1 --eslag-leaf-groups 2

  # Generate all formats and styles for 3-spine topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/3spine/default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/3spine/cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/3spine/hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/3spine/3spine.dot
  bin/hhfab diagram --format mermaid --output test-diagram/3spine/3spine.mermaid

  @echo "=== Generating diagrams for 4-mclag-2-orphan topology ==="
  bin/hhfab vlab gen --mclag-leafs-count 4 --orphan-leafs-count 2

  # Generate all formats and styles for 4-mclag-2-orphan topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/4mclag2orphan/default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/4mclag2orphan/cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/4mclag2orphan/hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/4mclag2orphan/4mclag2orphan.dot
  bin/hhfab diagram --format mermaid --output test-diagram/4mclag2orphan/4mclag2orphan.mermaid

  @echo "=== Generating diagrams for mesh topology ==="
  bin/hhfab vlab gen --mesh-links-count 2

  # Generate all formats and styles for mesh topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/mesh/default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/mesh/cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/mesh/hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/mesh/mesh.dot
  bin/hhfab diagram --format mermaid --output test-diagram/mesh/mesh.mermaid

  # Convert DRAWIO files to SVG
  @echo "=== Converting DRAWIO files to SVG ==="
  @if command -v drawio >/dev/null 2>&1; then \
    find test-diagram -name "*.drawio" -type f | while read DRAWIO_FILE; do \
      SVG_FILE="${DRAWIO_FILE}.svg"; \
      echo "Converting $DRAWIO_FILE to $SVG_FILE"; \
      drawio --export --format svg \
             --output "$SVG_FILE" \
             "$DRAWIO_FILE"; \
    done; \
  else \
    echo "WARNING: DrawIO CLI not available, skipping .drawio to SVG conversion"; \
  fi

  # Convert DOT files to SVG (with transparency)
  @echo "=== Converting DOT files to SVG ==="
  @if command -v dot >/dev/null 2>&1; then \
    find test-diagram -name "*.dot" -type f | while read DOT_FILE; do \
      SVG_FILE="${DOT_FILE}.svg"; \
      echo "Converting $DOT_FILE to $SVG_FILE"; \
      dot -Tsvg -Gbgcolor=transparent "$DOT_FILE" -o "$SVG_FILE"; \
    done; \
  else \
    echo "WARNING: GraphViz not available, skipping .dot to SVG conversion"; \
  fi

  # Convert Mermaid files to SVG (with transparency)
  @echo "=== Converting Mermaid files to SVG ==="
  @if command -v mmdc >/dev/null 2>&1; then \
    find test-diagram -name "*.mermaid" -type f | while read MERMAID_FILE; do \
      SVG_FILE="${MERMAID_FILE%.mermaid}.mermaid.svg"; \
      echo "Converting $MERMAID_FILE to $SVG_FILE"; \
      mmdc -i "$MERMAID_FILE" -o "$SVG_FILE" -t default -b transparent; \
    done; \
  else \
    echo "WARNING: Mermaid CLI not available, skipping .mermaid to SVG conversion"; \
  fi

  @echo ""
  @echo "All diagrams generated in test-diagram/ directory"
  @find test-diagram -type f | sort
  @echo ""
  @echo "Summary of generated topology directories:"
  @echo "- Default VLAB topology: test-diagram/default/"
  @echo "- 3-spine VLAB topology: test-diagram/3spine/"
  @echo "- 4-mclag-2-orphan topology: test-diagram/4mclag2orphan/"
  @echo "- Mesh topology: test-diagram/mesh/"
  @if [ -n "{{vlab_workdir}}" ]; then echo "- Live diagrams (if VLAB in {{vlab_workdir}}): test-diagram/live/"; fi

  @just _create-html

  @echo "Quick viewers:"
  @echo "- HTML Multi-format viewer: test-diagram/diagram-viewer.html"
  @echo ""
  @echo "To view the HTML file:"
  @echo "   firefox test-diagram/diagram-viewer.html"
  @echo "   # or"
  @echo "   xdg-open test-diagram/diagram-viewer.html"
  @echo ""
  @echo "Usage examples:"
  @echo "   just test-diagram                                    # No live diagrams, interactive"
  @echo "   just test-diagram \"\" \"-y\"                           # No live diagrams, auto-confirm"
  @echo "   just test-diagram \"/path/to/vlab\" \"-y\"              # With live diagrams, auto-confirm"

# Clean up all generated diagrams
clean-diagram:
  @echo "Cleaning up generated diagrams..."
  @rm -rf test-diagram/
  @echo "Diagrams cleaned"

# Generate HTML viewer for all diagrams
_check-go:
  @command -v go >/dev/null 2>&1 || (echo "Go not installed. Install from https://golang.org" && exit 1)

# Check if hhfab is built, build if needed
_check-hhfab:
  @if [ ! -x "bin/hhfab" ]; then \
    echo "hhfab binary not found, building..."; \
    just hhfab-build; \
  else \
    echo "hhfab binary found at bin/hhfab"; \
  fi

# Update your existing target
_create-html: _check-go
  @echo "=== Creating HTML viewer with all diagrams ==="
  @go run hack/generate_viewer.go test-diagram
