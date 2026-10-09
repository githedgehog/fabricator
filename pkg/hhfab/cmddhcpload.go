// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/hhfab/dhcpload"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var ErrDHCPLoadFailed = errors.New("DHCP load test failed")

type DHCPLoadOpts struct {
	dhcpload.Config
	FakeProtocolBase string
	FakeVTEPBase     string // empty = no VTEP IPs
	FakeASNBase      uint32
	// Keep skips the cleanup of the fake switches, the VPC and the network namespace
	Keep bool
	// SkipReadyCheck skips waiting for the fabric to be ready before the test
	SkipReadyCheck bool
}

func DoVLABDHCPLoad(ctx context.Context, workDir, cacheDir string, opts DHCPLoadOpts) error {
	c, _, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.DHCPLoad(ctx, opts)
}

//nolint:gocyclo,funlen
func (c *Config) DHCPLoad(ctx context.Context, opts DHCPLoadOpts) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := &opts.Config
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validating config: %w", err)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must be run as root to create the network namespace and bind the DHCP relay port") //nolint:err113
	}
	if len(c.Controls) == 0 {
		return fmt.Errorf("no control node in the config") //nolint:err113
	}

	fakeOpts := dhcpload.FakeSwitchOpts{ASNBase: opts.FakeASNBase}
	var err error
	if fakeOpts.ProtocolBase, err = netip.ParseAddr(opts.FakeProtocolBase); err != nil {
		return fmt.Errorf("parsing fake protocol base: %w", err)
	}
	if opts.FakeVTEPBase != "" {
		if fakeOpts.VTEPBase, err = netip.ParseAddr(opts.FakeVTEPBase); err != nil {
			return fmt.Errorf("parsing fake VTEP base: %w", err)
		}
	}

	control := c.Controls[0]
	controlPrefix, err := control.Spec.Management.IP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control node management IP: %w", err)
	}
	mgmtPrefix, err := c.Fab.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing management subnet: %w", err)
	}
	dhcpStart, _ := c.Fab.Spec.Config.Fabric.ManagementDHCPStart.Parse()
	dhcpEnd, _ := c.Fab.Spec.Config.Fabric.ManagementDHCPEnd.Parse()
	if err := dhcpload.CheckManagement(cfg.RelayIPs(), mgmtPrefix, dhcpStart, dhcpEnd, controlPrefix.Addr()); err != nil {
		return fmt.Errorf("checking relay IPs: %w", err)
	}
	cfg.Server = netip.AddrPortFrom(controlPrefix.Addr(), 67)

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	existing := &vpcapi.VPC{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: cfg.VPCName(), Namespace: kmetav1.NamespaceDefault}, existing); err == nil {
		if existing.Labels[dhcpload.FakeLabel] == "" {
			return fmt.Errorf("VPC %s already exists and wasn't created by this command, use a different --vpc", cfg.VPCName()) //nolint:err113
		}
	} else if kclient.IgnoreNotFound(err) != nil {
		return fmt.Errorf("getting VPC %s: %w", cfg.VPCName(), err)
	}

	// leftovers from an earlier run with --keep would keep the fabric not ready
	if err := removeDHCPLoadObjects(ctx, kube); err != nil {
		return err
	}

	if !opts.SkipReadyCheck {
		if err := WaitReady(ctx, kube, WaitReadyOpts{Timeout: 10 * time.Minute}); err != nil {
			return fmt.Errorf("waiting for fabric ready: %w", err)
		}
	}

	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}
	var template *wiringapi.Switch
	for _, sw := range switches.Items {
		if sw.Spec.Role.IsLeaf() && sw.Spec.IP != "" {
			template = &sw

			break
		}
	}
	if template == nil {
		return fmt.Errorf("no leaf switch found to clone fake leaves from") //nolint:err113
	}

	fakes, err := dhcpload.BuildFakeSwitches(cfg, template, fakeOpts)
	if err != nil {
		return fmt.Errorf("building fake switches: %w", err)
	}
	if err := dhcpload.CheckSwitchConflicts(fakes, switches.Items); err != nil {
		return fmt.Errorf("checking fake switches: %w", err)
	}

	if err := c.checkDHCPLoadNamespaces(ctx, kube, cfg, template); err != nil {
		return err
	}

	// from here on the cluster is changed
	if !opts.Keep {
		defer func() {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			slog.Info("Cleaning up")
			if err := removeDHCPLoadObjects(cctx, kube); err != nil {
				slog.Error("Cleaning up failed", "err", err)
			}
		}()
	}

	slog.Info("Creating fake leaf switches", "count", len(fakes), "template", template.Name)
	for _, fake := range fakes {
		some := &wiringapi.Switch{ObjectMeta: kmetav1.ObjectMeta{Name: fake.Name, Namespace: fake.Namespace}}
		if _, err := ctrlutil.CreateOrUpdate(ctx, kube, some, func() error {
			some.Labels = fake.Labels
			some.Spec = fake.Spec
			some.Default()

			return nil
		}); err != nil {
			return fmt.Errorf("creating fake switch %s: %w", fake.Name, err)
		}
	}

	vpc := dhcpload.BuildVPC(cfg)
	slog.Info("Creating VPC", "name", vpc.Name, "subnets", len(vpc.Spec.Subnets))
	someVPC := &vpcapi.VPC{ObjectMeta: kmetav1.ObjectMeta{Name: vpc.Name, Namespace: vpc.Namespace}}
	if _, err := ctrlutil.CreateOrUpdate(ctx, kube, someVPC, func() error {
		someVPC.Labels = vpc.Labels
		someVPC.Spec = vpc.Spec
		someVPC.Default()

		return nil
	}); err != nil {
		return fmt.Errorf("creating VPC %s: %w", vpc.Name, err)
	}

	if err := waitDHCPSubnets(ctx, kube, cfg); err != nil {
		return err
	}

	slog.Info("Setting up network namespace", "name", dhcpload.DefaultNetNS, "bridge", VLABBridge, "relays", cfg.Leaves)
	ns, err := dhcpload.SetupNetNS(dhcpload.DefaultNetNS, VLABBridge, cfg.RelayIPs(), controlPrefix.Bits())
	if err != nil {
		return fmt.Errorf("setting up netns: %w", err)
	}
	if !opts.Keep {
		defer func() {
			if err := ns.Cleanup(); err != nil {
				slog.Error("Removing netns failed", "err", err)
			}
		}()
	}

	if err := ns.CheckReachable(ctx, netip.AddrPortFrom(controlPrefix.Addr(), 22), 10*time.Second); err != nil {
		return fmt.Errorf("control node unreachable (is the VLAB management bridge forwarding? a Docker FORWARD DROP policy breaks it): %w", err)
	}

	var res *dhcpload.Result
	if err := ns.Do(func() error {
		var err error
		res, err = dhcpload.Run(ctx, cfg)

		return err //nolint:wrapcheck
	}); err != nil {
		return fmt.Errorf("running load test: %w", err)
	}
	if !res.OK {
		return fmt.Errorf("%w: clients=%d bound=%d failed=%d renews_failed=%d duplicate_ips=%d", ErrDHCPLoadFailed,
			res.Clients, res.Bound, res.Failed, res.RenewsFailed, res.DuplicateIPs)
	}

	return nil
}

func (c *Config) checkDHCPLoadNamespaces(ctx context.Context, kube kclient.Reader, cfg *dhcpload.Config, leaf *wiringapi.Switch) error {
	var vlans []meta.VLANRange
	for _, name := range leaf.Spec.VLANNamespaces {
		ns := &wiringapi.VLANNamespace{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, ns); err != nil {
			return fmt.Errorf("getting VLAN namespace %s: %w", name, err)
		}
		vlans = append(vlans, ns.Spec.Ranges...)
	}

	// the VPC uses the default IPv4 namespace, there is no per-leaf one
	v4 := &vpcapi.IPv4Namespace{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: "default", Namespace: kmetav1.NamespaceDefault}, v4); err != nil {
		return fmt.Errorf("getting IPv4 namespace default: %w", err)
	}
	ipv4 := make([]netip.Prefix, 0, len(v4.Spec.Subnets))
	for _, s := range v4.Spec.Subnets {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("parsing IPv4 namespace subnet %q: %w", s, err)
		}
		ipv4 = append(ipv4, p)
	}

	return dhcpload.CheckNamespaces(cfg, ipv4, vlans) //nolint:wrapcheck
}

func waitDHCPSubnets(ctx context.Context, kube kclient.Reader, cfg *dhcpload.Config) error {
	want := cfg.NumSubnets()
	slog.Info("Waiting for DHCPSubnets", "count", want)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for {
		missing := []int{}
		for i := range want {
			if err := kube.Get(ctx, kclient.ObjectKey{Name: cfg.DHCPSubnetName(i), Namespace: kmetav1.NamespaceDefault}, &dhcpapi.DHCPSubnet{}); err != nil {
				if kclient.IgnoreNotFound(err) != nil && ctx.Err() == nil {
					return fmt.Errorf("getting DHCPSubnet %s: %w", cfg.DHCPSubnetName(i), err)
				}
				missing = append(missing, i)
			}
		}
		if len(missing) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for DHCPSubnets, still missing %d (first: %s): %w", len(missing), cfg.DHCPSubnetName(slices.Min(missing)), ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// removeDHCPLoadObjects deletes the VPC and the fake switches created by the test, if any
func removeDHCPLoadObjects(ctx context.Context, kube kclient.Client) error {
	if err := kube.DeleteAllOf(ctx, &vpcapi.VPC{}, kclient.InNamespace(kmetav1.NamespaceDefault),
		kclient.MatchingLabels{dhcpload.FakeLabel: "true"}); err != nil {
		return fmt.Errorf("deleting VPCs: %w", err)
	}
	if err := kube.DeleteAllOf(ctx, &wiringapi.Switch{}, kclient.InNamespace(kmetav1.NamespaceDefault),
		kclient.MatchingLabels{dhcpload.FakeLabel: "true"}); err != nil {
		return fmt.Errorf("deleting fake switches: %w", err)
	}

	return nil
}
