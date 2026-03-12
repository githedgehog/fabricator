#!/bin/bash

set -e
set -euo pipefail

echo "====================="
echo "Setting up servers..."
echo "====================="
echo -e "\n"

{{ range $name, $server := $.Servers }}

echo -e "\nSetting up server: {{ $name }}"

SSHPASS='nvidia' sshpass -e ssh-copy-id -o StrictHostKeyChecking=accept-new -i ~/.ssh/id_rsa.pub ubuntu@{{ $name }}

cat <<'EOF' | ssh ubuntu@{{ $name }} bash
hostname
{{ range $ifaceName, $iface := $server.Ifaces }}
sudo ip link set dev {{ $ifaceName }} up
sudo ip a flush dev {{ $ifaceName }}
sudo ip a a {{ $iface.IP }} dev {{ $ifaceName }}
{{ end }}
{{ range $prefix, $route := $server.Routes }}
sudo ip r a {{ $prefix }}{{ range $via := $route.NextHops }} nexthop via {{ $via }}{{ end }}
{{ end }}
EOF

{{ end }}

echo -e "\n"
echo "==================="
echo "All servers set up."
echo "==================="
