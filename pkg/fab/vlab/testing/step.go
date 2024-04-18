// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package testing

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/hashicorp/go-multierror"
	"github.com/melbahja/goph"
	"github.com/pkg/errors"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/client/apiabbr"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VLABStepHelper struct {
	kube       client.WithWatch
	sshPorts   map[string]uint
	sshKeyPath string
}

var _ StepHelper = (*VLABStepHelper)(nil)

func NewVLABStepHelper(kube client.WithWatch, sshPorts map[string]uint, sshKeyPath string) *VLABStepHelper {
	return &VLABStepHelper{
		kube:       kube,
		sshPorts:   sshPorts,
		sshKeyPath: sshKeyPath,
	}
}

func (h *VLABStepHelper) Kube() client.WithWatch {
	return h.kube
}

func (h *VLABStepHelper) ServerExec(ctx context.Context, server, cmd string, timeout time.Duration) (string, error) {
	port, ok := h.sshPorts[server]
	if !ok {
		return "", errors.Errorf("ssh port for server %s not found", server)
	}

	// TODO think about default timeouts
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	auth, err := goph.Key(h.sshKeyPath, "")
	if err != nil {
		return "", errors.Wrapf(err, "error loading SSH key %s", h.sshKeyPath)
	}

	client, err := goph.NewConn(&goph.Config{
		User:     "core",
		Addr:     "127.0.0.1",
		Port:     port,
		Auth:     auth,
		Timeout:  5 * time.Second,             // TODO think about TCP dial timeout
		Callback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	})
	if err != nil {
		return "", errors.Wrapf(err, "error creating SSH client for server %s", server)
	}

	// TODO autoinject client side timeout?
	out, err := client.RunContext(ctx, cmd)
	if err != nil {
		return string(out), errors.Wrapf(err, "error running command on server %s using ssh", server)
	}

	return strings.TrimSpace(string(out)), nil
}

type StepWaitReady struct {
	Timeout Duration `json:"timeout,omitempty"`
}

var _ Step = (*StepWaitReady)(nil)

func (s *StepWaitReady) Run(ctx context.Context, h StepHelper) error {
	slog.Info("Running wait ready step")

	expected := []string{}
	switches := &wiringapi.SwitchList{}
	if err := h.Kube().List(ctx, switches); err != nil {
		return errors.Wrap(err, "error listing switches")
	}
	for _, sw := range switches.Items {
		expected = append(expected, sw.Name)
	}

	return WaitForSwitchesReady(ctx, h.Kube(), expected, 5*time.Minute) // TODO make configurable
}

type StepAPIAbbr struct {
	loader func() (*apiabbr.Enforcer, error)
}

var _ Step = (*StepAPIAbbr)(nil)

func (s *StepAPIAbbr) Run(ctx context.Context, h StepHelper) error {
	slog.Info("Running api abbr step")

	enf, err := s.loader()
	if err != nil {
		return err
	}

	return errors.Wrapf(enf.Enforce(ctx, h.Kube()), "error enforcing")
}

type StepNetconf struct {
	toolbox sync.Mutex
}

var _ Step = (*StepNetconf)(nil)

func (s *StepNetconf) Run(ctx context.Context, h StepHelper) error {
	slog.Info("Running netconf step")

	servers := &wiringapi.ServerList{}
	if err := h.Kube().List(ctx, servers); err != nil {
		return errors.Wrap(err, "error listing servers")
	}

	g := multierror.Group{}
	for _, srv := range servers.Items {
		if srv.IsControl() {
			continue
		}

		srvName := srv.Name
		netconfs, err := buildNetconf(ctx, h.Kube(), srvName)
		if err != nil {
			return errors.Wrapf(err, "error building netconf for server %s", srvName)
		}

		g.Go(withLog(func() error {
			return s.setupNetwork(ctx, h, srvName, netconfs)
		}, "Setup netconf", "server", srvName))
	}

	if err := g.Wait(); err != nil { // TODO think about error handling
		return errors.New("error setting up netconf")
	}

	return nil
}

func (s *StepNetconf) setupNetwork(ctx context.Context, h StepHelper, srv string, netconfs []netconf) error {
	if err := s.checkHostnameAndWarmupToolbox(ctx, h, srv); err != nil {
		return err
	}

	if len(netconfs) > 1 {
		return errors.Errorf("multiple netconf (vpc attachment) per server not supported")
	}

	netconfs = append([]netconf{{cmd: "cleanup"}}, netconfs...)
	for _, nc := range netconfs {
		out, err := h.ServerExec(ctx, srv, "/opt/bin/hhnet "+nc.cmd, 30*time.Second) // TODO timeout
		if err != nil {
			return errors.Wrapf(err, "error running netconf on server %s", srv)
		}

		if nc.subnet == "" {
			if out != "" {
				return errors.Errorf("unexpected output from server %s netconf: %s", srv, out)
			}

			continue
		}

		ip, ipNet, err := net.ParseCIDR(out)
		if err != nil {
			return errors.Wrapf(err, "error parsing server IP %s", out)
		}

		if ipNet.String() != nc.subnet {
			return errors.Errorf("server received IP from %s, but expected from %s", ipNet.String(), nc.subnet)
		}

		slog.Debug("Server IP", "server", srv, "subnet", nc.subnet, "ip", ip.String())
	}

	return nil
}

func (s *StepNetconf) checkHostnameAndWarmupToolbox(ctx context.Context, h StepHelper, srv string) error {
	s.toolbox.Lock()
	defer s.toolbox.Unlock()

	out, err := h.ServerExec(ctx, srv, "toolbox -q hostname", 10*time.Second) // TODO timeout
	if err != nil {
		return errors.Wrapf(err, "error getting hostname for server %s", srv)
	}

	if strings.Contains(out, "/var/lib/toolbox") {
		out, err = h.ServerExec(ctx, srv, "toolbox -q hostname", 10*time.Second) // TODO timeout
		if err != nil {
			return errors.Wrapf(err, "error getting hostname for server %s", srv)
		}
	}

	if out != srv {
		return errors.Errorf("server %s hostname %s doesn't match server name", srv, out)
	}

	return nil
}

type StepTestConnectivity struct {
	PingCount    uint    `json:"pingCount,omitempty"`
	IPerfSeconds uint    `json:"iperfSeconds,omitempty"`
	IPerfSpeed   float64 `json:"iperfSpeed,omitempty"`

	ipDiscovery sync.Mutex
	ips         map[string]string

	toolbox sync.Mutex
}

var _ Step = (*StepTestConnectivity)(nil)

func (s *StepTestConnectivity) Run(ctx context.Context, h StepHelper) error {
	slog.Info("Running test connectivity step")

	servers := &wiringapi.ServerList{}
	if err := h.Kube().List(ctx, servers); err != nil {
		return errors.Wrap(err, "error listing servers")
	}

	g := multierror.Group{}
	for _, source := range servers.Items {
		if source.IsControl() {
			continue
		}

		sourceName := source.Name
		for _, target := range servers.Items {
			if target.IsControl() {
				continue
			}
			if source.Name == target.Name {
				continue
			}

			targetName := target.Name
			serverReachable, err := apiutil.IsServerReachable(ctx, h.Kube(), sourceName, targetName)
			if err != nil {
				return errors.Wrapf(err, "error checking connectivity")
			}

			g.Go(withDebugLog(func() error {
				return s.testServerReachable(ctx, h, sourceName, targetName, serverReachable)
			}, "Test server reachable", "source", sourceName, "target", targetName, "reachable", serverReachable))
		}

		extReachable, err := apiutil.IsExternalSubnetReachable(ctx, h.Kube(), sourceName, "0.0.0.0/0")
		if err != nil {
			return errors.Wrapf(err, "error checking external connectivity")
		}

		g.Go(withDebugLog(func() error {
			return s.testExternalReachable(ctx, h, sourceName, extReachable)
		}, "Test external reachable", "source", sourceName, "reachable", extReachable))
	}

	slog.Debug("All connectivity tests started")

	if err := g.Wait(); err.ErrorOrNil() != nil { // TODO think about error handling
		return errors.New("error testing connectivity")
	}

	return nil
}

func (s *StepTestConnectivity) testServerReachable(ctx context.Context, h StepHelper, source, target string, expectedReachable bool) error {
	targetIP, err := s.getServerIP(ctx, h, target)
	if err != nil {
		return errors.Wrapf(err, "error getting IP for server %s", target)
	}

	// TODO handle case when there is no IP on a server
	if targetIP == "" {
		return errors.Errorf("no IP found for server %s", target)
	}

	cmd := fmt.Sprintf("ping -c %d -W 1 %s", s.PingCount, targetIP) // TODO timeout

	out, err := h.ServerExec(ctx, source, cmd, time.Duration(s.PingCount+5)*time.Second) // TODO timeout

	pingOk := err == nil && strings.Contains(out, "0% packet loss")
	if expectedReachable && !pingOk {
		return errors.Errorf("should be reachable but ping failed with output: %s", out)
	}

	pingFail := err != nil && strings.Contains(out, "100% packet loss")
	if !expectedReachable && !pingFail {
		return errors.Errorf("should not be reachable but ping succeeded, err: %s", err)
	}

	// TODO handle error

	slog.Debug("ping report", "source", source, "target", target, "targetIP", targetIP, "reachable", expectedReachable)

	if !expectedReachable || s.IPerfSeconds == 0 {
		return nil
	}

	s.toolbox.Lock()
	defer s.toolbox.Unlock()

	g := multierror.Group{}

	g.Go(func() error {
		cmd := fmt.Sprintf("toolbox -q timeout -v %d iperf3 -s -1", s.IPerfSeconds+17)
		out, err := h.ServerExec(ctx, target, cmd, time.Duration(s.IPerfSeconds+20)*time.Second) // TODO timeout
		if err != nil {
			return errors.Wrapf(err, "error starting iperf server with cmd %q: %s", cmd, out)
		}

		return nil
	})

	g.Go(func() error {
		time.Sleep(2 * time.Second) // TODO think about more reliable way to wait for server to start

		cmd = fmt.Sprintf("toolbox -q timeout -v %d iperf3 -J -c %s -t %d", s.IPerfSeconds+5, targetIP, s.IPerfSeconds)
		out, err := h.ServerExec(ctx, source, cmd, time.Duration(s.IPerfSeconds+10)*time.Second) // TODO timeout
		if err != nil {
			return errors.Wrapf(err, "error running iperf client with cmd %q: %s", cmd, out)
		}

		report, err := ParseIperf3Report(out)
		if err != nil {
			return errors.Wrapf(err, "error parsing iperf report")
		}

		slog.Debug("iperf3 report", "source", source, "target", target, "targetIP", targetIP,
			"sentSpeed", humanize.Bytes(uint64(report.End.SumSent.BitsPerSecond/8))+"/s",
			"receivedSpeed", humanize.Bytes(uint64(report.End.SumReceived.BitsPerSecond/8))+"/s",
			"sent", humanize.Bytes(uint64(report.End.SumSent.Bytes)),
			"received", humanize.Bytes(uint64(report.End.SumReceived.Bytes)),
		)

		if report.End.SumSent.BitsPerSecond < s.IPerfSpeed*1000000 {
			return errors.Errorf("iperf speed too low: %s < %s",
				humanize.Bytes(uint64(report.End.SumSent.BitsPerSecond/8))+"/s", // TODO print in Mbps?
				humanize.Bytes(uint64(s.IPerfSpeed)*1000000),
			)
		}

		return nil
	})

	return g.Wait().ErrorOrNil() //nolint:wrapcheck
}

func (s *StepTestConnectivity) testExternalReachable(ctx context.Context, h StepHelper, source string, expectedReachable bool) error {
	cmd := "timeout -v 30 curl --insecure https://8.8.8.8" // TODO make configurable

	out, err := h.ServerExec(ctx, source, cmd, 32*time.Second) // TODO timeout

	curlOk := err == nil && strings.Contains(out, "302 Moved")
	if expectedReachable && !curlOk {
		return errors.Errorf("should be reachable but curl failed with output: %s", out)
	}

	curlFail := err != nil && strings.Contains(out, "Failed to connect")
	if !expectedReachable && !curlFail {
		return errors.Errorf("should not be reachable but curl succeeded with output: %s", out)
	}

	// TODO handle error

	return nil
}

func (s *StepTestConnectivity) getServerIP(ctx context.Context, h StepHelper, srv string) (string, error) {
	s.ipDiscovery.Lock()
	defer s.ipDiscovery.Unlock()

	if s.ips == nil {
		s.ips = map[string]string{}
	}

	if ip, ok := s.ips[srv]; ok {
		return ip, nil
	}

	out, err := h.ServerExec(ctx, srv, "ip a s | grep 'inet 10\\.' | awk '/inet / {print $2}'", 5*time.Second) // TODO timeout
	if err != nil {
		return "", errors.Wrapf(err, "error getting IP for server %s", srv)
	}

	ip := ""
	if out != "" {
		netIP, _, err := net.ParseCIDR(out)
		if err != nil {
			return "", errors.Wrapf(err, "error parsing IP for server %s", srv)
		}

		ip = netIP.String()
	}

	s.ips[srv] = ip

	return ip, nil
}

func withLog(f func() error, msg string, args ...any) func() error {
	return func() error {
		err := f()
		if err != nil {
			slog.Error(msg+" failure", append(args, "err", err.Error())...)
		} else {
			slog.Info(msg+" success", args...)
		}

		return err
	}
}

func withDebugLog(f func() error, msg string, args ...any) func() error {
	return func() error {
		err := f()
		if err != nil {
			slog.Error(msg+" failure", append(args, "err", err.Error())...)
		} else {
			slog.Debug(msg+" success", args...)
		}

		return err
	}
}
