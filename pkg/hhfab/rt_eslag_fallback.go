// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	eslagPxeDHCPTimeoutSec = 20
	eslagPxeAttemptsPerLeg = 3
)

func eslagFallbackTest(ctx context.Context, testCtx *VPCPeeringTestCtx, _ *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3VNI {
		return true, nil, fmt.Errorf("L3VNI mode is not compatible with ESLAG") //nolint:goerr113
	}

	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeESLAG}); err != nil {
		return false, nil, fmt.Errorf("listing ESLAG connections: %w", err)
	}

	candidates := make([]*wiringapi.Connection, 0)
	for i := range conns.Items {
		c := &conns.Items[i]
		if c.Spec.ESLAG != nil && len(c.Spec.ESLAG.Links) >= 2 {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return true, nil, errNoEslags
	}

	if !testCtx.extended {
		candidates = candidates[:1]
	}

	reverts := make([]RevertFunc, 0)
	ranAtLeastOnce := false
	for _, conn := range candidates {
		vlan, server, err := findESLAGAttachedVLAN(ctx, testCtx.kube, conn)
		if err != nil {
			return false, reverts, fmt.Errorf("finding VPC attachment for connection %s: %w", conn.Name, err)
		}
		if vlan == 0 {
			slog.Info("ESLAG connection has no VPC attachment in the active suite, skipping", "connection", conn.Name)

			continue
		}

		if err := enableESLAGFallback(ctx, testCtx, conn.Name, &reverts); err != nil {
			return false, reverts, err
		}

		slaves := make([]string, 0, len(conn.Spec.ESLAG.Links))
		leafForSlave := make(map[string]string, len(conn.Spec.ESLAG.Links))
		for _, link := range conn.Spec.ESLAG.Links {
			slave := link.Server.LocalPortName()
			slaves = append(slaves, slave)
			leafForSlave[slave] = link.Switch.DeviceName()
		}

		slog.Info("Testing port-channel fallback on ESLAG", "connection", conn.Name, "server", server, "vlan", vlan, "legs", slaves)

		ssh, err := testCtx.getSSH(ctx, server)
		if err != nil {
			return false, reverts, fmt.Errorf("getting ssh config for %s: %w", server, err)
		}

		netconfCmd, err := GetServerNetconfCmd(conn, ServerNetconfOpts{
			VLAN:       vlan,
			HashPolicy: testCtx.setupOpts.HashPolicy,
		})
		if err != nil {
			return false, reverts, fmt.Errorf("building netconf command for %s: %w", server, err)
		}
		reverts = append(reverts, func(ctx context.Context) error {
			return restoreServerBond(ctx, ssh, netconfCmd)
		})

		failures := make([]string, 0)
		for _, slave := range slaves {
			ok := 0
			for i := 0; i < eslagPxeAttemptsPerLeg; i++ {
				got := pxeAttempt(ctx, ssh, slave, vlan, eslagPxeDHCPTimeoutSec)
				if got {
					ok++
				}
			}
			slog.Info("Port-channel fallback leg result", "server", server, "slave", slave, "leaf", leafForSlave[slave], "ok", ok, "attempts", eslagPxeAttemptsPerLeg)
			if ok == 0 {
				failures = append(failures, fmt.Sprintf("slave=%s leaf=%s connection=%s", slave, leafForSlave[slave], conn.Name))
			}
			ranAtLeastOnce = true
		}

		if len(failures) > 0 {
			return false, reverts, fmt.Errorf("port-channel fallback DHCP not relayed on legs: %s", strings.Join(failures, "; ")) //nolint:goerr113
		}
	}

	if !ranAtLeastOnce {
		return true, reverts, fmt.Errorf("no ESLAG connection with a VPC attachment found") //nolint:goerr113
	}

	return false, reverts, nil
}

func enableESLAGFallback(ctx context.Context, testCtx *VPCPeeringTestCtx, name string, reverts *[]RevertFunc) error {
	latest := &wiringapi.Connection{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: name}, latest); err != nil {
		return fmt.Errorf("getting Connection %s: %w", name, err)
	}
	if latest.Spec.ESLAG == nil {
		return fmt.Errorf("connection %s has no ESLAG spec", name) //nolint:goerr113
	}
	originalFallback := latest.Spec.ESLAG.Fallback
	if originalFallback {
		return nil
	}

	slog.Info("Enabling ESLAG fallback for test", "connection", name)
	latest.Spec.ESLAG.Fallback = true
	if err := testCtx.kube.Update(ctx, latest); err != nil {
		return fmt.Errorf("updating Connection %s to enable fallback: %w", name, err)
	}

	*reverts = append(*reverts, func(ctx context.Context) error {
		restoring := &wiringapi.Connection{}
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: name}, restoring); err != nil {
			return fmt.Errorf("re-fetching Connection %s for revert: %w", name, err)
		}
		if restoring.Spec.ESLAG == nil || restoring.Spec.ESLAG.Fallback == originalFallback {
			return nil
		}
		restoring.Spec.ESLAG.Fallback = originalFallback
		if err := testCtx.kube.Update(ctx, restoring); err != nil {
			return fmt.Errorf("restoring Connection %s fallback: %w", name, err)
		}

		return WaitReady(ctx, testCtx.kube, testCtx.wrOpts)
	})

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return fmt.Errorf("waiting for switches after enabling fallback on %s: %w", name, err)
	}

	return nil
}

// findESLAGAttachedVLAN returns the VLAN for the connection's VPC attachment, or 0 if none.
func findESLAGAttachedVLAN(ctx context.Context, kube kclient.Client, conn *wiringapi.Connection) (uint16, string, error) {
	_, serverNames, _, _, err := conn.Spec.Endpoints() //nolint:dogsled
	if err != nil || len(serverNames) != 1 {
		return 0, "", fmt.Errorf("connection %s does not have a single server endpoint", conn.Name) //nolint:goerr113
	}
	server := serverNames[0]

	attaches := &vpcapi.VPCAttachmentList{}
	if err := kube.List(ctx, attaches, kclient.MatchingLabels{wiringapi.LabelConnection: conn.Name}); err != nil {
		return 0, server, fmt.Errorf("listing VPCAttachments: %w", err)
	}
	for _, a := range attaches.Items {
		vpc := &vpcapi.VPC{}
		if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: a.Spec.VPCName()}, vpc); err != nil {
			continue
		}
		subnet := vpc.Spec.Subnets[a.Spec.SubnetName()]
		if subnet == nil || subnet.HostBGP {
			continue
		}

		return subnet.VLAN, server, nil
	}

	return 0, server, nil
}

// pxeAttempt bounds hhnet's 5-minute DHCP wait with a timeout so a
// non-relaying leg fails fast.
func pxeAttempt(ctx context.Context, ssh *sshutil.Config, slave string, vlan uint16, timeoutSec int) bool {
	cmd := fmt.Sprintf("/opt/bin/hhnet cleanup >/dev/null 2>&1 || true; timeout %d /opt/bin/hhnet vlan %d %s", timeoutSec, vlan, slave)
	stdout, _, err := ssh.Run(ctx, cmd)
	if err != nil {
		return false
	}

	return hasLeasedIPv4(stdout)
}

func restoreServerBond(ctx context.Context, ssh *sshutil.Config, netconfCmd string) error {
	if _, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
		return fmt.Errorf("cleaning up interfaces: %w (stderr: %s)", err, stderr)
	}
	if _, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet "+netconfCmd); err != nil {
		return fmt.Errorf("restoring bond (%s): %w (stderr: %s)", netconfCmd, err, stderr)
	}

	return nil
}

// hasLeasedIPv4 scans hhnet stdout for an "<ip>/<prefix>" success line.
func hasLeasedIPv4(out string) bool {
	for _, tok := range strings.Fields(out) {
		addr := tok
		if i := strings.IndexByte(addr, '/'); i >= 0 {
			addr = addr[:i]
		}
		if ip, err := netip.ParseAddr(addr); err == nil && ip.Is4() {
			return true
		}
	}

	return false
}
