// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/hhfab/bmc"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu"
	"golang.org/x/sync/errgroup"
	coreapi "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// VLABHWDir keeps the markers of the hardware switches and nodes installed into this VLAB
	VLABHWDir             = "hw"
	VLABHWInstalledSuffix = ".installed"
)

var (
	ErrBMCCredentials = errors.New("BMC username and password are required to manage hardware nodes")
	ErrMediaServer    = errors.New("media server failed")
)

// checkBMCNodes checks the credentials are set and each BMC has a local address to fetch the media from
func checkBMCNodes(ctx context.Context, nodes map[string]string, opts BMCOpts) error {
	if len(nodes) == 0 {
		return nil
	}

	if opts.Username == "" || opts.Password == "" {
		return ErrBMCCredentials
	}

	for name, endpoint := range nodes {
		if _, err := bmc.LocalAddrFor(ctx, endpoint); err != nil {
			return fmt.Errorf("node %s: %w", name, err)
		}
	}

	return nil
}

func nodeISOName(name string) string {
	return string(recipe.TypeNode) + recipe.Separator + name + recipe.Separator + recipe.InstallISOImageSuffix
}

// serveNodeISOs runs the media server helper (root for port 80) until stopped; the returned context is done when it
// stops, with ErrMediaServer as the cause if it failed
func (c *Config) serveNodeISOs(ctx context.Context, nodes []string) (context.Context, func(), error) {
	id := rand.Text()

	args := []string{"serve-media", "--id", id}
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		args = append(args, "--verbose")
	}
	for _, name := range nodes {
		iso := nodeISOName(name)
		args = append(args, iso+"="+filepath.Join(c.WorkDir, ResultDir, iso))
	}

	srvCtx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)

		err := execLongHelper(srvCtx, c.WorkDir, args)
		if srvCtx.Err() != nil {
			return
		}
		if err == nil {
			err = fmt.Errorf("exited") //nolint:err113
		}

		slog.Error("Media server failed", "err", err)
		cancel(fmt.Errorf("%w: %w", ErrMediaServer, err))
	}()
	stop := func() {
		cancel(nil)
		<-done
	}

	// fail right away if it can't start, e.g. port 80 is taken
	localURL := bmc.MediaURL(netip.AddrFrom4([4]byte{127, 0, 0, 1}), "")
	deadline := time.After(time.Minute)
	for !bmc.Serving(srvCtx, localURL, id) {
		select {
		case <-srvCtx.Done():
			err := context.Cause(srvCtx)
			stop()

			return nil, nil, fmt.Errorf("starting media server: %w", err)
		case <-deadline:
			stop()

			return nil, nil, fmt.Errorf("starting media server: not serving on %s", localURL) //nolint:err113
		case <-time.After(250 * time.Millisecond):
		}
	}

	return srvCtx, stop, nil
}

// hwNodeInstall installs the hardware node from the ISO over BMC, it's cancelled when mediaCtx is done (nil if the
// server is in another process)
func (c *Config) hwNodeInstall(ctx, mediaCtx context.Context, vlab *VLAB, d *artificer.Downloader, name, endpoint string, opts VLABRunOpts) (err error) {
	vm := VM{Name: name, Type: VMTypeGateway}

	if mediaCtx != nil {
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(ctx)
		defer cancel(nil)
		defer context.AfterFunc(mediaCtx, func() {
			cancel(context.Cause(mediaCtx))
		})()
	}

	// the node joins the existing cluster, so the ISO built by this run has to be the same version; the one served by
	// another process is whatever it built, so it's reinstalled as is
	var kube kclient.Client
	if !vlab.Created {
		var cluster *fabapi.Fabricator
		kube, cluster, err = c.clusterFab(ctx)
		if err != nil {
			return err
		}
		if mediaCtx != nil {
			if err := c.checkClusterVersion(cluster); err != nil {
				return err
			}
		}
	}

	if err := c.resetHWInstalled(name); err != nil {
		return err
	}

	// the media server failure cancels the install, so report why
	defer func() {
		if cause := context.Cause(ctx); err != nil && errors.Is(cause, ErrMediaServer) && !errors.Is(err, cause) {
			err = fmt.Errorf("%w: %w", err, cause)
		}
	}()

	addr, err := bmc.LocalAddrFor(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("getting local address for BMC: %w", err)
	}
	isoURL := bmc.MediaURL(addr, nodeISOName(name))
	if err := bmc.CheckMedia(ctx, isoURL); err != nil {
		return fmt.Errorf("checking installer ISO is served (is vlab up running?): %w", err)
	}

	client, err := bmc.Connect(ctx, endpoint, opts.BMC.Username, opts.BMC.Password)
	if err != nil {
		return fmt.Errorf("connecting to BMC: %w", err)
	}
	defer client.Close()

	slog.Info("Booting hardware node from the installer ISO over BMC", "node", name, "bmc", endpoint)

	if err := client.EjectAll(ctx); err != nil {
		return fmt.Errorf("ejecting virtual media: %w", err)
	}
	if err := client.InsertCD(ctx, isoURL); err != nil {
		return fmt.Errorf("inserting installer ISO: %w", err)
	}
	if err := client.BootOnceFromCD(); err != nil {
		return fmt.Errorf("setting boot override: %w", err)
	}
	resetAt := time.Now()
	if err := client.Reset(ctx); err != nil {
		return fmt.Errorf("resetting node: %w", err)
	}

	bootCtx, bootCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer bootCancel()
	if err := client.WaitBootOverrideConsumed(bootCtx); err != nil {
		return fmt.Errorf("booting from the installer ISO: %w", err)
	}

	slog.Info("Hardware node booted from the installer ISO", "node", name)

	// the old OS is down now, so it won't register the node again
	if kube != nil {
		if err := deleteK8sNode(ctx, kube, name); err != nil {
			return err
		}
	}

	isoOpts := opts
	isoOpts.BuildMode = recipe.BuildModeISO
	if err := c.vmPostProcess(ctx, vlab, d, vm, isoOpts); err != nil {
		return err
	}

	ssh, err := c.SSHVM(ctx, vlab, vm)
	if err != nil {
		return fmt.Errorf("creating ssh client: %w", err)
	}
	out, _, err := ssh.Run(ctx, "stat -c %Y "+recipe.InstallMarkerFile+" && date +%s")
	if err != nil {
		return fmt.Errorf("checking install marker time: %w", err)
	}
	if err := checkFreshInstall(out, time.Now(), resetAt); err != nil {
		return err
	}

	// the node boots from the disk anyway
	if err := client.EjectAll(ctx); err != nil {
		slog.Warn("Failed to eject virtual media after install", "node", name, "err", err)
	}

	if err := c.setHWInstalled(name); err != nil {
		return err
	}

	slog.Info("Hardware node installed", "node", name)

	return nil
}

// installClockSlack is how much earlier than the reset a fresh install marker may look
const installClockSlack = 2 * time.Minute

// checkFreshInstall checks the install marker is newer than the reset, otherwise the node booted the old OS; out is
// the marker mtime and the node time in unix seconds, so the node clock doesn't matter
func checkFreshInstall(out string, now, resetAt time.Time) error {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return fmt.Errorf("unexpected install marker time output %q", out) //nolint:err113
	}

	markerUnix, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return fmt.Errorf("parsing install marker time %q: %w", fields[0], err)
	}
	nodeUnix, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parsing node time %q: %w", fields[1], err)
	}

	// marker age on the node clock applied to the local one
	marker := now.Add(-time.Duration(nodeUnix-markerUnix) * time.Second)
	if marker.Before(resetAt.Add(-installClockSlack)) {
		return fmt.Errorf("install marker is from %s, before the reset at %s: node booted the old OS instead of the installer ISO", //nolint:err113
			marker.Format(time.RFC3339), resetAt.Format(time.RFC3339))
	}

	return nil
}

// selectBMCNodes returns all BMC nodes if no names given, unused ones only by name
func (c *Config) selectBMCNodes(names []string, withUnused bool) (map[string]string, error) {
	all, err := c.bmcNodes()
	if err != nil {
		return nil, err
	}
	if withUnused && len(names) > 0 {
		for _, node := range c.UnusedHW.Nodes {
			endpoint, err := getBMCEndpoint(&node)
			if err != nil {
				return nil, err
			}
			if endpoint != "" {
				all[node.Name] = endpoint
			}
		}
	}

	nodes := all
	if len(names) > 0 {
		nodes = map[string]string{}
		for _, name := range names {
			endpoint, ok := all[name]
			if !ok {
				return nil, fmt.Errorf("node %q isn't a hardware node with %s annotation", name, HHFabCfgBMC) //nolint:err113
			}
			nodes[name] = endpoint
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no hardware nodes with %s annotation", HHFabCfgBMC) //nolint:err113
	}

	return nodes, nil
}

func (c *Config) VLABNodePower(ctx context.Context, opts NodePowerOpts) error {
	nodes, err := c.selectBMCNodes(opts.Nodes, true)
	if err != nil {
		return err
	}

	if opts.BMC.Username == "" || opts.BMC.Password == "" {
		return ErrBMCCredentials
	}

	slog.Info("Power managing nodes", "action", opts.Action, "nodes", slices.Sorted(maps.Keys(nodes)))

	errs := []error{}
	for _, name := range slices.Sorted(maps.Keys(nodes)) {
		if err := powerNode(ctx, nodes[name], opts.Action, opts.BMC); err != nil {
			slog.Error("Failed to power manage node", "node", name, "action", opts.Action, "err", err)
			errs = append(errs, fmt.Errorf("node %s: %w", name, err))

			continue
		}

		slog.Info("Node power action done", "node", name, "action", opts.Action)
	}

	return errors.Join(errs...)
}

func powerNode(ctx context.Context, endpoint string, action pdu.Action, creds BMCOpts) error {
	client, err := bmc.Connect(ctx, endpoint, creds.Username, creds.Password)
	if err != nil {
		return fmt.Errorf("connecting to BMC: %w", err)
	}
	defer client.Close()

	switch action {
	case pdu.ActionOn:
		err = client.PowerOn(ctx)
	case pdu.ActionOff:
		err = client.PowerOff(ctx)
	case pdu.ActionCycle:
		err = client.Reset(ctx)
	default:
		return fmt.Errorf("unsupported power action %q", action) //nolint:err113
	}
	if err != nil {
		return fmt.Errorf("powering %s: %w", action, err)
	}

	return nil
}

func (c *Config) VLABNodeReinstall(ctx context.Context, vlab *VLAB, opts NodeReinstallOpts) error {
	nodes, err := c.selectBMCNodes(opts.Nodes, false)
	if err != nil {
		return err
	}

	if err := checkBMCNodes(ctx, nodes, opts.BMC); err != nil {
		return err
	}

	for name := range nodes {
		iso := nodeISOName(name)
		if _, err := os.Stat(filepath.Join(c.WorkDir, ResultDir, iso)); err != nil {
			slog.Error("Build the installer ISO first", "cmd", "hhfab build")

			return fmt.Errorf("checking installer ISO %s: %w", iso, err)
		}
	}

	// the media server is part of the running vlab up
	runOpts := VLABRunOpts{BMC: opts.BMC}

	// no shared context, so a failing node doesn't cancel the others mid-install
	group := &errgroup.Group{}
	for name, endpoint := range nodes {
		group.Go(func() error {
			if err := c.hwNodeInstall(ctx, nil, vlab, nil, name, endpoint, runOpts); err != nil {
				return fmt.Errorf("reinstalling node %s: %w", name, err)
			}

			return nil
		})
	}

	return group.Wait() //nolint:wrapcheck
}

// clusterFab waits for the cluster K8s API, the control node may be still installing or booting
func (c *Config) clusterFab(ctx context.Context) (kclient.Client, *fabapi.Fabricator, error) {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	for idx := 0; ; idx++ {
		reason := "no kubeconfig yet"
		if _, err := os.Stat(kubeconfig); err == nil {
			kube, err := kubeutil.NewClientWithCore(ctx, kubeconfig, fabapi.AddToScheme, agentapi.AddToScheme)
			if err == nil {
				f := &fabapi.Fabricator{}
				if err = kube.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f); err == nil {
					return kube, f, nil
				}
			}
			// only the message, the kubeutil errors carry a stack trace
			reason = err.Error()
		}

		if idx%8 == 0 { // every ~2 min
			slog.Debug("Waiting for K8s API", "reason", reason)
		}

		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("waiting for K8s API: %w: %s", ctx.Err(), reason)
		case <-time.After(15 * time.Second):
		}
	}
}

// checkClusterVersion is needed as the node gets the fab CA from the node config agent only if its airgap image is the
// cluster's one
func (c *Config) checkClusterVersion(cluster *fabapi.Fabricator) error {
	clusterV := cluster.Status.Versions.Fabricator.NodeConfig
	installerV := c.Fab.Status.Versions.Fabricator.NodeConfig
	if clusterV != installerV {
		slog.Error("Existing cluster and node installer versions differ, upgrade the cluster first or recreate it",
			"cluster", clusterV, "installer", installerV, "upgrade", "hhfab vlab up --upgrade", "recreate", "hhfab vlab up -f")

		return fmt.Errorf("existing cluster is at %s, node installer at %s", clusterV, installerV) //nolint:err113
	}

	return nil
}

// deleteK8sNode removes the node and its k3s node password, so it can join again after the reinstall
func deleteK8sNode(ctx context.Context, kube kclient.Client, name string) error {
	if err := kube.Delete(ctx, &coreapi.Node{Name: name}); kclient.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting node %s: %w", name, err)
	}

	secret := &coreapi.Secret{Name: name + ".node-password.k3s", Namespace: kmetav1.NamespaceSystem}
	if err := kube.Delete(ctx, secret); kclient.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting node %s password: %w", name, err)
	}

	slog.Info("Hardware node removed from K8s before reinstall", "node", name)

	return nil
}
