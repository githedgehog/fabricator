# Show-Tech Diagnostic Collection System

## Overview

The show-tech system collects comprehensive diagnostic information from all fabric nodes (switches, servers, control nodes, and gateways) during VLAB testing. It's automatically triggered when tests fail or when explicitly requested via `--collect-show-tech` flag.

## Architecture

### Components

1. **Bash Scripts** (this directory)
   - `switch.sh` - SONiC switch diagnostics
   - `server.sh` - Flatcar Linux server diagnostics
   - `gateway.sh` - Gateway node diagnostics (OS + FRR/vtysh)
   - `control.sh` - Kubernetes control plane diagnostics
   - `runner.sh` - CI runner diagnostics (local execution)

2. **Go Orchestration Code**
   - `pkg/hhfab/vlabhelpers.go` - Collection orchestration
   - `pkg/hhfab/release.go` - Test failure integration

### Execution Flow

```
Test Failure Detected
    ↓
collectShowTechForTest() called
    ↓
For each node (switch/server/control/gateway):
    1. SSH to node
    2. Upload appropriate script to /tmp/show-tech.sh
    3. Execute script remotely (150s timeout)
    4. Download /tmp/show-tech.log (full diagnostics)
    5. Download /tmp/show-tech-errors.log (error summary)
    ↓
For runner (if applicable):
    1. Execute runner.sh locally
    2. Save output to runner-show-tech.log
    ↓
All files saved to: show-tech-output/{timestamp}/
```

## Output Files

### Directory Structure

```
Working directory root:
├── vlab-timeline.txt              # Execution timeline (Phase 2.2)

show-tech-output/
└── 2025-01-06T12-34-56/          # Timestamp of collection
    ├── _test-failure-context.txt  # Test metadata and analysis hints
    ├── leaf-01-show-tech.log      # Full switch diagnostics
    ├── leaf-01-show-tech-errors.log  # Error summary
    ├── server-1-show-tech.log
    ├── server-1-show-tech-errors.log
    ├── control-1-show-tech.log
    ├── control-1-show-tech-errors.log
    ├── gateway-1-show-tech.log    # If gateway enabled
    ├── gateway-1-show-tech-errors.log
    └── runner-show-tech.log       # Runner diagnostics
```

### File Descriptions

#### `*-show-tech.log` (Full Diagnostics)
- Complete diagnostic output from each node
- Size: Typically 1-10 MB per file
- Contains: All command outputs, logs, configuration dumps

#### `*-show-tech-errors.log` (Error Summary)
- Extracted errors, warnings, and failures
- Size: Typically 10-100 KB per file
- **Start here for analysis** - provides quick overview
- Contains:
  - Extracted ERR/FAIL/WARN lines (first 500)
  - Summary counts
  - Node-type-specific issue sections
  - Top 10 most common error patterns (normalized)

#### `_test-failure-context.txt` (Test Metadata)
- Test name, VPC configuration, timing settings
- Created only when show-tech is collected after test failure
- Provides context for understanding what was being tested

#### `vlab-timeline.txt` (Execution Timeline)
- Chronological log of VLAB execution events with elapsed time
- **Start here for timing analysis** - shows sequence and duration
- Contains:
  - VM boot and readiness events
  - K8s node join and readiness
  - OnReady command execution (SetupVPCs, Inspect, tests)
  - WaitReady calls with stabilization periods
  - Test start/completion/failure markers
- Format: `+MM:SS [TAG] Event description`
- Example:
  ```
  +03:35 [WAIT] WaitReady started (applied-for: 15s, stabilization: 0s)
  +03:50 [WAIT] WaitReady completed
  +03:50 [TEST] SetupVPCs started
  +04:15 [TEST] SetupVPCs completed
  +04:26 [TEST] Inspect started
  +04:26 [FAIL] Inspect failed
  ```
- Immediately reveals timing issues like insufficient stabilization periods

## Error Extraction Details

Each script extracts errors and generates patterns tailored to its node type:

### switch.sh
**Extracts:**
- ERR, FAIL, WARN from SONiC logs
- Filters out false positives (e.g., "err-disabled" interface status)

**Specific Sections:**
- Summary counts

**Pattern Normalization:**
- Timestamps → `TIME`
- VLAN names → `VlanX`
- IP addresses → `IP`

**Use for:** BGP EVPN issues, VXLAN problems, interface failures, VRF/VLAN configuration errors

### server.sh
**Extracts:**
- Network errors, ping failures, interface issues
- iperf3 problems

**Specific Sections:**
- Ping failures (100% packet loss, unreachable)
- Network interface issues (DOWN, no carrier, link down)

**Pattern Normalization:**
- Timestamps → `TIME`
- IP addresses → `IP`
- NIC names → `NIC`

**Use for:** Connectivity failures, VLAN tagging issues, bonding problems

### control.sh
**Extracts:**
- Kubernetes pod errors (CrashLoopBackOff, ImagePullBackOff, Pending, Evicted)
- K8s warning events
- API server health issues

**Specific Sections:**
- Pod issues
- Kubernetes events (FailedScheduling, FailedMount, BackOff, Unhealthy)
- API server health (healthz/readyz/livez failures)

**Pattern Normalization:**
- Timestamps → `TIME`
- IP addresses → `IP`
- Pod names → `pod/POD`
- UUIDs → `UUID`

**Use for:** Fabric controller issues, agent problems, k3s failures

### gateway.sh
**Extracts:**
- BGP session state issues (Idle, Active, down)
- FRR/zebra errors
- Interface problems

**Specific Sections:**
- BGP session issues (Idle, Active (Connect), down, notifications)
- Interface issues (down, no carrier)
- FRR errors (zebra, bgpd failures)

**Pattern Normalization:**
- Timestamps → `TIME`
- IP addresses → `IP`
- BGP neighbors → `neighbor PEER`
- AS numbers → `AS ASN`

**Use for:** External BGP peering failures, route redistribution issues, FRR crashes

## Code Locations

### Embedding and Script Loading
```go
// pkg/hhfab/vlabhelpers.go

//go:embed show-tech/switch.sh
var switchScript []byte

//go:embed show-tech/server.sh
var serverScript []byte

//go:embed show-tech/control.sh
var controlScript []byte

//go:embed show-tech/gateway.sh
var gatewayScript []byte

//go:embed show-tech/runner.sh
var runnerScript []byte

func DefaultShowTechScript() map[string][]byte // Line ~533
```

### Collection Orchestration
```go
// pkg/hhfab/vlabhelpers.go

func (c *Config) collectShowTech(
    ctx context.Context,
    entryName string,
    ssh *sshutil.Config,
    script []byte,
    outputDir string,
) error // Line ~680

func (c *Config) collectRunnerShowTech(
    ctx context.Context,
    outputDir string,
) error // Line ~728
```

### Test Integration
```go
// pkg/hhfab/release.go

func (testCtx *VPCPeeringTestCtx) collectShowTechForTest(
    test *JUnitTestCase,
) // Line ~3753

func (testCtx *VPCPeeringTestCtx) writeTestFailureContext(
    outputDir string,
    test *JUnitTestCase,
) // Line ~3794
```

### VLAB Runner Integration
```go
// pkg/hhfab/vlabrunner.go

// Search for "collectShowTech" to find where collection is triggered
// during VLAB up command execution
```

## Modifying Show-Tech Scripts

### Adding New Diagnostic Commands

1. Edit the appropriate `.sh` script in this directory
2. Add commands to existing sections or create new section:
   ```bash
   # ---------------------------
   # New Section Name
   # ---------------------------
   {
       echo -e "\n=== New Section ==="
       your-command-here
   } >> "$OUTPUT_FILE" 2>&1
   ```
3. Rebuild hhfab: `just hhfab-build`
4. Scripts are embedded at build time via go:embed

### Modifying Error Extraction

Each script has an error extraction section at the end. To modify:

1. Edit the grep patterns to match new error types
2. Add new specific sections for your error category
3. Update pattern normalization sed commands if needed
4. Test locally by running script and checking error file

Example:
```bash
# Add to specific sections
echo ""
echo "=== MY NEW ERROR CATEGORY ==="
grep -E "my-error-pattern" "$OUTPUT_FILE" 2>/dev/null | head -20 || echo "No issues detected"
```

### Adding New Node Types

1. Create new script: `pkg/hhfab/show-tech/newtype.sh`
2. Add go:embed directive in `vlabhelpers.go`:
   ```go
   //go:embed show-tech/newtype.sh
   var newtypeScript []byte
   ```
3. Update `DefaultShowTechScript()` function
4. Add collection logic in appropriate location

## Debugging Collection Issues

### Script Execution Failures

If script fails to run on remote node:
1. Check script syntax: `bash -n script.sh`
2. Verify commands exist on target OS (SONiC vs Flatcar vs Ubuntu)
3. Check timeout (150s) in `collectShowTech()` - increase if needed
4. Review stderr output in debug logs

### Missing Error Files

Error files might be missing if:
- Script is from older version (pre-error-extraction)
- Script failed before reaching error extraction section
- Download failed (check SSH connectivity)

The collection code handles this gracefully with debug logging.

## Quick Reference

### Common Analysis Workflow

1. **Start with `_test-failure-context.txt`**
   - Understand what test failed and why
   - Note VPC configuration and timing

2. **Review `vlab-timeline.txt`**
   - Check event sequence and timing
   - Look for insufficient stabilization periods
   - Identify when failure occurred relative to setup
   - Spot timing-related issues (too fast, too slow)

3. **Check `*-errors.log` files**
   - Start with switches (usually root cause)
   - Check error counts and specific sections
   - Review top error patterns

4. **Deep dive into `*-show-tech.log`** (if needed)
   - Use error file as guide to find relevant sections
   - Search for specific error messages or timestamps

5. **Cross-reference between nodes**
   - Compare timestamps across nodes
   - Look for error propagation (switch → server)

### Grep Patterns for Quick Analysis

```bash
# Find all BGP issues across all switches
grep -h "BGP" show-tech-output/*/leaf-*-errors.log

# Find timing of errors on specific switch
grep -h "TIME" show-tech-output/*/leaf-01-show-tech.log | head -50

# Count errors per node type
for f in show-tech-output/*/*.log; do
    echo "$f: $(grep -c ERR "$f")";
done | sort -t: -k2 -rn
```
