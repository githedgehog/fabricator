# hhfab CLI Documentation

# NAME

hhfab - hedgehog fabricator - build, install and run hedgehog

# SYNOPSIS

hhfab

# DESCRIPTION

Create Hedgehog configs, wiring diagram, build an installer and optionally run the virtual lab (VLAB):
	1.  Initialize working dir by running 'hhfab init', to use default creds use '--dev' (unsafe)
	2a. If building for physical environment, use 'hhfab sample' to generate sample wiring diagram
	2b. If building for VLAB, use 'hhfab vlab gen' to generate VLAB wiring diagram
	3.  Validate configs and wiring with 'hhfab validate' at any time (optional)
	4.  Build Hedgehog installer with 'hhfab build'
	5.  Use 'hhfab vlab up' to run VLAB (will run build automatically if needed)
		

**Usage**:

```
hhfab [GLOBAL OPTIONS] [command [COMMAND OPTIONS]] [ARGUMENTS...]
```

# COMMANDS

## init

initializes working dir (current dir by default) with a new fab.yaml and other files

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--config, -c**="": use existing config file `PATH`

**--default-authorized-keys, --keys**="": default authorized `KEYS` for control and switch users (default: [])

**--default-password-hash, --passwd**="": default password `HASH` for control and switch users

**--dev**: use default dev credentials (unsafe)

**--fabric-mode, --mode, -m**="": set fabric mode: one of collapsed-core, spine-leaf (default: spine-leaf)

**--force, -f**: overwrite existing files

**--registry-prefix**="": prepend artifact names with `PREFIX` (default: githedgehog)

**--registry-repo**="": download artifacts from `REPO` (default: ghcr.io)

**--tls-san, --tls**="": IPs and DNS names that will be used to access API (default: [])

**--verbose, -v**: verbose output (includes debug)

**--wiring, -w**="": include wiring diagram `FILE` with ext .yaml (any Fabric API objects) (default: [])

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

## validate

validate config and wiring files

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--hydrate-mode, --hm**="": set hydrate mode: one of never, if-not-present, override (default: if-not-present)

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

## diagram

generate a diagram to visualze topology

    Generate network topology diagrams in different formats from your wiring diagram.
    
    			FORMATS:
    			   drawio (default) - Creates a diagram.io file that can be opened with https://app.diagrams.net/
    			                      You can edit the diagram and export to various formats including PNG, SVG, PDF.
    
    			   dot             - Creates a Graphviz DOT file that can be rendered using Graphviz tools:
    			                      - Install Graphviz: https://graphviz.org/download/
    			                      - Convert to PNG: 'dot -Tpng vlab-diagram.dot -o vlab-diagram.png'
    			                      - Convert to SVG: 'dot -Tsvg vlab-diagram.dot -o vlab-diagram.svg'
    			                      - Convert to PDF: 'dot -Tpdf vlab-diagram.dot -o vlab-diagram.pdf'
    
    			   mermaid         - Not currently supported.
    
    			EXAMPLES:
    			   # Generate default draw.io diagram
    			   hhfab diagram
    
    			   # Generate dot diagram for graphviz
    			   hhfab diagram --format dot
    
    			   # Generate draw.io diagram with custom style
    			   hhfab diagram --format drawio --style hedgehog

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--format, -f**="": diagram format: drawio (default), dot (graphviz), mermaid (unsupported) (default: drawio)

**--style, -s**="": diagram style (only applies to drawio format): default, cisco, hedgehog (default: default)

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

## versions

print versions of all components

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--hydrate-mode, --hm**="": set hydrate mode: one of never, if-not-present, override (default: if-not-present)

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

## build

build installers

**--brief, -b**: brief output (only warn and error)

**--build-controls, --controls**: build control node(s)

**--build-mode, --mode, -m**="": build mode: one of manual, usb, iso (default: iso)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--hydrate-mode, --hm**="": set hydrate mode: one of never, if-not-present, override (default: if-not-present)

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

## vlab

operate Virtual Lab

### generate, gen

generate VLAB wiring diagram

**--brief, -b**: brief output (only warn and error)

**--bundled-servers**="": number of bundled servers to generate for switches (only for one of the second switch in the redundancy group or orphan switch) (default: 1)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--eslag-leaf-groups**="": eslag leaf groups (comma separated list of number of ESLAG switches in each group, should be 2-4 per group, e.g. 2,4,2 for 3 groups with 2, 4 and 2 switches)

**--eslag-servers**="": number of ESLAG servers to generate for ESLAG switches (default: 2)

**--fabric-links-count**="": number of fabric links if fabric mode is spine-leaf (default: 0)

**--mclag-leafs-count**="": number of mclag leafs (should be even) (default: 0)

**--mclag-peer-links**="": number of mclag peer links for each mclag leaf (default: 0)

**--mclag-servers**="": number of MCLAG servers to generate for MCLAG switches (default: 2)

**--mclag-session-links**="": number of mclag session links for each mclag leaf (default: 0)

**--no-switches**: do not generate any switches

**--orphan-leafs-count**="": number of orphan leafs (default: 0)

**--spines-count**="": number of spines if fabric mode is spine-leaf (default: 0)

**--unbundled-servers**="": number of unbundled servers to generate for switches (only for one of the first switch in the redundancy group or orphan switch) (default: 1)

**--verbose, -v**: verbose output (includes debug)

**--vpc-loopbacks**="": number of vpc loopbacks for each switch (default: 0)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### up

run VLAB

**--auto-upgrade, --upgrade**: automatically upgrade all node(s), expected to be used after initial successful installation

**--brief, -b**: brief output (only warn and error)

**--build-mode, --mode, -m**="": build mode: one of manual, usb, iso (default: iso)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--collect-show-tech, --collect**: collect show-tech from all devices at exit or error

**--controls-restricted**: restrict control nodes from having access to the host (effectively access to internet)

**--fail-fast**: exit on first error

**--hydrate-mode, --hm**="": set hydrate mode: one of never, if-not-present, override (default: if-not-present)

**--kill-stale**: kill stale VMs automatically based on VM UUIDs used

**--ready, -r**="": run commands on all VMs ready (one of: exit, setup-vpcs, switch-reinstall, test-connectivity, wait, inspect, release-test) (default: [])

**--recreate, -f**: recreate VLAB (destroy and create new config and VMs)

**--servers-restricted**: restrict server nodes from having access to the host (effectively access to internet)

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### ssh

ssh to a VLAB VM or HW if supported

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--name, -n**="": name of the VM or HW to access

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### serial

get serial console of a VLAB VM or HW if supported

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--name, -n**="": name of the VM or HW to access

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### seriallog

get serial console log of a VLAB VM or HW if supported

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--name, -n**="": name of the VM or HW to access

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### show-tech

collect diagnostic information from all VLAB devices

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### setup-vpcs, vpcs

setup VPCs and VPCAttachments for all servers and configure networking on them

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--dns-servers, --dns**="": DNS servers for VPCs advertised by DHCP (default: [])

**--force-cleanup, -f**: start with removing all existing VPCs and VPCAttachments

**--interface-mtu, --mtu**="": interface MTU for VPCs advertised by DHCP (default: 0)

**--ipns**="": IPv4 namespace for VPCs (default: default)

**--name, -n**="": name of the VM or HW to access

**--servers-per-subnet, --servers**="": number of servers per subnet (default: 1)

**--subnets-per-vpc, --subnets**="": number of subnets per VPC (default: 1)

**--time-servers, --ntp**="": Time servers for VPCs advertised by DHCP (default: [])

**--verbose, -v**: verbose output (includes debug)

**--vlanns**="": VLAN namespace for VPCs (default: default)

**--wait-switches-ready, --wait**: wait for switches to be ready before and after configuring VPCs and VPCAttachments

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### setup-peerings, peers

setup VPC and External Peerings per requests (remove all if empty)

    Setup test scenario with VPC/External Peerings by specifying requests in the format described below.
    
    Example command:
    
    $ hhfab vlab setup-peerings 1+2 2+4:r=border 1~as5835 2~as5835:subnets=sub1,sub2:prefixes=0.0.0.0/0,22.22.22.0/24
    
    Which will produce:
    1. VPC peering between vpc-01 and vpc-02
    2. Remote VPC peering between vpc-02 and vpc-04 on switch group named border
    3. External peering for vpc-01 with External as5835 with default vpc subnet and any routes from external permitted
    4. External peering for vpc-02 with External as5835 with subnets sub1 and sub2 exposed from vpc-02 and default route
       from external permitted as well any route that belongs to 22.22.22.0/24
    
    VPC Peerings:
    
    1+2 -- VPC peering between vpc-01 and vpc-02
    demo-1+demo-2 -- VPC peering between vpc-demo-1 and vpc-demo-2
    1+2:r -- remote VPC peering between vpc-01 and vpc-02 on switch group if only one switch group is present
    1+2:r=border -- remote VPC peering between vpc-01 and vpc-02 on switch group named border
    1+2:remote=border -- same as above
    
    External Peerings:
    
    1~as5835 -- external peering for vpc-01 with External as5835
    1~ -- external peering for vpc-1 with external if only one external is present for ipv4 namespace of vpc-01, allowing
    	default subnet and any route from external
    1~:subnets=default@prefixes=0.0.0.0/0 -- external peering for vpc-1 with auth external with default vpc subnet and
    	default route from external permitted
    1~as5835:subnets=default,other:prefixes=0.0.0.0/0_le32_ge32,22.22.22.0/24 -- same but with more details
    1~as5835:s=default,other:p=0.0.0.0/0_le32_ge32,22.22.22.0/24 -- same as above

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--name, -n**="": name of the VM or HW to access

**--verbose, -v**: verbose output (includes debug)

**--wait-switches-ready, --wait**: wait for switches to be ready before and after configuring peerings

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### test-connectivity, conns

test connectivity between servers

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--curls**="": number of curl tests to run for each server to test external connectivity (0 to disable) (default: 3)

**--destination, --dst**="": server to use as destination for connectivity tests (default: all servers) (default: [])

**--iperfs**="": seconds of iperf3 test to run between each pair of reachable servers (0 to disable) (default: 10)

**--iperfs-speed**="": minimum speed in Mbits/s for iperf3 test to consider successful (0 to not check speeds) (default: 8200)

**--name, -n**="": name of the VM or HW to access

**--pings**="": number of pings to send between each pair of servers (0 to disable) (default: 5)

**--source, --src**="": server to use as source for connectivity tests (default: all servers) (default: [])

**--verbose, -v**: verbose output (includes debug)

**--wait-switches-ready, --wait**: wait for switches to be ready before testing connectivity

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### wait-switches, wait

wait for all switches to be ready

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### inspect-switches, inspect

wait for ready and inspect all switches

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--strict**: fail if any switch is not ready or not inspected

**--verbose, -v**: verbose output (includes debug)

**--wait-applied-for, --wait, -w**="": wait for switches being applied for this duration in seconds (0 to only wait for ready) (default: 120)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### release-test

run release tests on current VLAB instance

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--extended, -e**: run extended tests

**--fail-fast, -f**: stop testing on first failure

**--invert-regex, -i**: invert regex match

**--pause-on-fail, -p**: pause testing on each scenario failure (for troubleshooting)

**--regex, -r**="": run only tests matched by regular expression. can be repeated (default: [])

**--results-file**="": path to a file to export test results to in JUnit XML format

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

### switch

manage switch reinstall or power

**--brief, -b**: brief output (only warn and error)

**--cache-dir**="": use cache dir `DIR` for caching downloaded files (default: /home/pau/.hhfab-cache)

**--name, -n**="": name of the VM or HW to access

**--verbose, -v**: verbose output (includes debug)

**--workdir**="": run as if hhfab was started in `PATH` instead of the current working directory (default: /home/pau/fabricator)

#### reinstall

reboot/reset and reinstall NOS on switches (if no switches specified, all switches will be reinstalled)

**--mode, -m**="": restart mode: reboot, hard-reset (default: hard-reset)

**--name, -n**="": switch name to reinstall (default: [])

**--pdu-password**="": PDU password to attempt a reboot (hard-reset mode only)

**--pdu-username**="": PDU username to attempt a reboot (hard-reset mode only)

**--switch-password**="": switch password to attempt a reboot (reboot mode only, prompted for if empty)

**--switch-username**="": switch username to attempt a reboot (reboot mode only, prompted for if empty)

**--verbose, -v**: verbose output (includes debug)

**--wait-ready, -w**: wait until switch(es) are Fabric-ready

**--yes, -y**: assume yes

#### power

manage switch power state using the PDU (if no switches specified, all switches will be affected)

**--action, -a**="": power action: one of on, off, cycle (default: cycle)

**--name, -n**="": switch name to manage power (default: [])

**--pdu-password**="": PDU password to attempt a reboot (hard-reset mode only)

**--pdu-username**="": PDU username to attempt a reboot (hard-reset mode only)

**--verbose, -v**: verbose output (includes debug)

**--yes, -y**: assume yes
