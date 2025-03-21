// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/melbahja/goph"
	"github.com/pkg/sftp"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/util/butaneutil"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
)

//go:embed vlab_butane.tmpl.yaml
var serverButaneTmpl string

//go:embed vlab_frr_butane.tmpl.yaml
var frrButaneTmpl string

//go:embed hhnet.sh
var hhnet []byte

const (
	VLABOSImageFile  = "os.img"
	VLABEFICodeFile  = "efi_code.fd"
	VLABEFIVarsFile  = "efi_vars.fd"
	VLABUSBImageFile = "usb.img"
	VLABISOImageFile = "usb.iso"

	VLABSerialLog  = "serial.log"
	VLABSerialSock = "serial.sock"
	VLABMonSock    = "mon.sock"
	VLABQMPSock    = "qmp.sock"

	VLABCmdSudo       = "sudo"
	VLABCmdQemuImg    = "qemu-img"
	VLABCmdQemuSystem = "qemu-system-x86_64"
	VLABCmdSocat      = "socat"
	VLABCmdSSH        = "ssh"
	VLABCmdLess       = "less"
	VLABCmdExpect     = "expect"

	VLABIgnition = "ignition.json"

	VLABKubeConfig = "kubeconfig"

	VLABEnvPDUUsername = "HHFAB_VLAB_PDU_USERNAME"
	VLABEnvPDUPassword = "HHFAB_VLAB_PDU_PASSWORD" //nolint:gosec
)

var VLABCmds = []string{
	VLABCmdSudo,
	VLABCmdQemuImg,
	VLABCmdQemuSystem,
	VLABCmdSocat,
	VLABCmdSSH,
	VLABCmdLess,
}

type VLABRunOpts struct {
	KillStale          bool
	ControlsRestricted bool
	ServersRestricted  bool
	BuildMode          recipe.BuildMode
	ControlUpgrade     bool
	FailFast           bool
	OnReady            []string
	CollectShowTech    bool
	SpawnExternal      bool
}

type OnReady string

const (
	OnReadyExit             OnReady = "exit"
	OnReadySetupVPCs        OnReady = "setup-vpcs"
	OnReadySwitchReinstall  OnReady = "switch-reinstall"
	OnReadyTestConnectivity OnReady = "test-connectivity"
	OnReadyWait             OnReady = "wait"
	OnReadyInspect          OnReady = "inspect"
	OnReadyReleaseTest      OnReady = "release-test"
)

var AllOnReady = []OnReady{
	OnReadyExit,
	OnReadySetupVPCs,
	OnReadySwitchReinstall,
	OnReadyTestConnectivity,
	OnReadyWait,
	OnReadyInspect,
	OnReadyReleaseTest,
}

var ErrExit = fmt.Errorf("exit")

func (c *Config) checkForBins() error {
	for _, cmd := range VLABCmds {
		_, err := exec.LookPath(cmd)
		if err != nil {
			return fmt.Errorf("required command %q is not available", cmd) //nolint:goerr113
		}
	}

	return nil
}

func (c *Config) VLABRun(ctx context.Context, vlab *VLAB, opts VLABRunOpts) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	for _, cmd := range opts.OnReady {
		if !slices.Contains(AllOnReady, OnReady(cmd)) {
			return fmt.Errorf("unsupported on-ready command %q", cmd) //nolint:goerr113
		}
	}

	if len(opts.OnReady) > 0 && !opts.FailFast {
		slog.Warn("On-ready commands enables fail-fast")
		opts.FailFast = true
	}

	if err := c.checkForBins(); err != nil {
		return err
	}

	stale, err := CheckStaleVMs(ctx, false)
	if err != nil {
		return fmt.Errorf("checking for stale VMs: %w", err)
	}
	if len(stale) > 0 {
		if !opts.KillStale {
			return fmt.Errorf("%d stale or detached VM(s) found: rerun with --kill-stale for autocleanup", len(stale)) //nolint:goerr113
		}

		if err := execHelper(ctx, c.WorkDir, []string{"kill-stale-vms"}); err != nil {
			return fmt.Errorf("running helper to cleanup stale VMs: %w", err)
		}

		time.Sleep(1 * time.Second)

		stale, err := CheckStaleVMs(ctx, false)
		if err != nil {
			return fmt.Errorf("checking for stale VMs after cleanup: %w", err)
		}

		if len(stale) > 0 {
			return fmt.Errorf("%d stale or detached VM(s) found after cleanup", len(stale)) //nolint:goerr113
		}
	}

	if err := execHelper(ctx, c.WorkDir, []string{
		"setup-taps", "--count", fmt.Sprintf("%d", vlab.Taps),
	}); err != nil {
		return fmt.Errorf("running helper to prepare taps: %w", err)
	}

	toBind := []string{}
	for _, dev := range vlab.Passthroughs {
		if !isDeviceBoundToVFIO(dev) {
			toBind = append(toBind, dev)
		}
	}
	if len(toBind) > 0 {
		if err := execHelper(ctx, c.WorkDir, append([]string{"vfio-pci-bind"}, toBind...)); err != nil {
			return fmt.Errorf("running helper to bind devices: %w", err)
		}
	}

	cpu := uint(0)
	ram := uint(0)
	disk := uint(0)
	for _, vm := range vlab.VMs {
		cpu += vm.Size.CPU
		ram += vm.Size.RAM
		disk += vm.Size.Disk
	}

	d, err := artificer.NewDownloaderWithDockerCreds(c.CacheDir, c.Repo, c.Prefix)
	if err != nil {
		return fmt.Errorf("creating downloader: %w", err)
	}

	for _, vm := range vlab.VMs {
		if vm.Type == VMTypeExternal && !opts.SpawnExternal {
			continue
		}
		vmDir := filepath.Join(c.WorkDir, VLABDir, VLABVMsDir, vm.Name)

		if isPresent(vmDir, VLABOSImageFile, VLABEFICodeFile, VLABEFIVarsFile) {
			slog.Info("Using existing", "vm", vm.Name, "type", vm.Type)
		} else {
			slog.Info("Preparing new", "vm", vm.Name, "type", vm.Type)

			if err := os.MkdirAll(vmDir, 0o700); err != nil {
				return fmt.Errorf("creating VM dir %q: %w", vmDir, err)
			}

			resize := false
			if vm.Type == VMTypeControl && opts.BuildMode == recipe.BuildModeManual || vm.Type == VMTypeServer || vm.Type == VMTypeGateway || vm.Type == VMTypeExternal {
				resize = true

				if err := d.FromORAS(ctx, vmDir, vlabcomp.FlatcarRef, vlabcomp.FlatcarVersion(c.Fab), []artificer.ORASFile{
					{
						Name:   "flatcar.img",
						Target: VLABOSImageFile,
					},
					{
						Name:   "flatcar_efi_code.fd",
						Target: VLABEFICodeFile,
					},
					{
						Name:   "flatcar_efi_vars.fd",
						Target: VLABEFIVarsFile,
					},
				}); err != nil {
					return fmt.Errorf("copying flatcar files: %w", err)
				}
			} else if vm.Type == VMTypeControl && (opts.BuildMode == recipe.BuildModeUSB || opts.BuildMode == recipe.BuildModeISO) {
				if err := d.FromORAS(ctx, vmDir, vlabcomp.FlatcarRef, vlabcomp.FlatcarVersion(c.Fab), []artificer.ORASFile{
					{
						Name:   "flatcar_efi_code.fd",
						Target: VLABEFICodeFile,
					},
					{
						Name:   "flatcar_efi_vars.fd",
						Target: VLABEFIVarsFile,
					},
				}); err != nil {
					return fmt.Errorf("copying flatcar files: %w", err)
				}

				if err := execCmd(ctx, false, vmDir,
					VLABCmdQemuImg, []string{"create", "-f", "qcow2", VLABOSImageFile, fmt.Sprintf("%dG", vm.Size.Disk)},
					"vm", vm.Name); err != nil {
					return fmt.Errorf("creating empty os image: %w", err)
				}

				source, target := "", ""
				if opts.BuildMode == recipe.BuildModeUSB {
					source, target = vm.Name+recipe.InstallUSBImageSuffix, VLABUSBImageFile
				} else if opts.BuildMode == recipe.BuildModeISO {
					source, target = vm.Name+recipe.InstallISOImageSuffix, VLABISOImageFile
				}
				if err := artificer.CopyFile(
					filepath.Join(c.WorkDir, ResultDir, source),
					filepath.Join(vmDir, target),
				); err != nil {
					return fmt.Errorf("copying image: %w", err)
				}
			} else if vm.Type == VMTypeSwitch {
				resize = true

				if err := d.FromORAS(ctx, vmDir, vlabcomp.ONIERef, vlabcomp.ONIEVersion(c.Fab), []artificer.ORASFile{
					{
						Name:   "onie-kvm_x86_64.qcow2",
						Target: VLABOSImageFile,
					},
					{
						Name:   "onie_efi_code.fd",
						Target: VLABEFICodeFile,
					},
					{
						Name:   "onie_efi_vars.fd",
						Target: VLABEFIVarsFile,
					},
				}); err != nil {
					return fmt.Errorf("copying onie files: %w", err)
				}
			} else {
				return fmt.Errorf("unsupported VM type %q", vm.Type) //nolint:goerr113
			}

			if resize {
				if err := execCmd(ctx, false, vmDir,
					VLABCmdQemuImg, []string{"resize", VLABOSImageFile, fmt.Sprintf("%dG", vm.Size.Disk)},
					"vm", vm.Name); err != nil {
					return fmt.Errorf("resizing os image: %w", err)
				}
			}

			if vm.Type == VMTypeServer || vm.Type == VMTypeGateway {
				ign, err := serverIgnition(c.Fab, vm)
				if err != nil {
					return fmt.Errorf("generating ignition: %w", err)
				}

				if err := os.WriteFile(filepath.Join(vmDir, VLABIgnition), ign, 0o600); err != nil {
					return fmt.Errorf("writing ignition: %w", err)
				}
			} else if vm.Type == VMTypeExternal {
				but, ign, err := externalIgnition(c.Fab, vm, vlab.ExternalVRFs)
				if err != nil {
					return fmt.Errorf("generating external ignition: %w", err)
				}
				if err := os.WriteFile(filepath.Join(vmDir, "butane.yaml"), []byte(but), 0o600); err != nil {
					return fmt.Errorf("writing external butane: %w", err)
				}

				if err := os.WriteFile(filepath.Join(vmDir, VLABIgnition), ign, 0o600); err != nil {
					return fmt.Errorf("writing external ignition: %w", err)
				}
			}
		}
	}

	group, ctx := errgroup.WithContext(ctx)
	postProcesses := &sync.WaitGroup{}
	postProcessDone := make(chan struct{})

	for _, vm := range vlab.VMs {
		if vm.Type == VMTypeExternal && !opts.SpawnExternal {
			continue
		}
		vmDir := filepath.Join(c.WorkDir, VLABDir, VLABVMsDir, vm.Name)

		group.Go(func() error {
			args := []string{
				"-name", vm.Name,
				"-uuid", fmt.Sprintf(VLABUUIDTmpl, vm.ID),
				"-m", fmt.Sprintf("%dM", vm.Size.RAM),
				"-machine", "q35,accel=kvm,smm=on",
				"-cpu", "host",
				"-smp", fmt.Sprintf("%d", vm.Size.CPU),
				"-object", "rng-random,filename=/dev/urandom,id=rng0",
				"-device", "virtio-rng-pci,rng=rng0",
				"-drive", "if=none,file=os.img,id=disk1",
				"-device", "virtio-blk-pci,drive=disk1,bootindex=1",
				"-drive", "if=pflash,file=efi_code.fd,format=raw,readonly=on",
				"-drive", "if=pflash,file=efi_vars.fd,format=raw",
				"-display", "none",
				"-vga", "none",
				"-chardev", fmt.Sprintf("socket,id=serial,path=%s,server=on,wait=off,signal=off,logfile=%s", VLABSerialSock, VLABSerialLog),
				"-serial", "chardev:serial",
				"-monitor", fmt.Sprintf("unix:%s,server,nowait", VLABMonSock),
				"-qmp", fmt.Sprintf("unix:%s,server,nowait", VLABQMPSock),
				"-global", "ICH9-LPC.disable_s3=1",
			}

			// for detached:
			// -daemonize
			// -pidfile

			if vm.Type == VMTypeControl && opts.BuildMode == recipe.BuildModeManual || vm.Type == VMTypeServer || vm.Type == VMTypeGateway || vm.Type == VMTypeExternal {
				ign := VLABIgnition
				if vm.Type == VMTypeControl {
					ign = filepath.Join(c.WorkDir, ResultDir, vm.Name+recipe.InstallIgnitionSuffix)
				}
				args = append(args,
					"-fw_cfg", "name=opt/org.flatcar-linux/config,file="+ign,
				)
			}

			if vm.Type == VMTypeControl && opts.BuildMode == recipe.BuildModeUSB {
				args = append(args,
					"-drive", fmt.Sprintf("if=none,format=raw,file=%s,id=disk2", VLABUSBImageFile),
					"-device", "virtio-blk-pci,drive=disk2,bootindex=2",
				)
			}

			if vm.Type == VMTypeControl && opts.BuildMode == recipe.BuildModeISO {
				args = append(args,
					"-device", "virtio-scsi-pci,id=scsi0",
					"-device", "scsi-cd,bus=scsi0.0,drive=cdrom0,bootindex=2",
					"-drive", fmt.Sprintf("id=cdrom0,if=none,readonly=on,file=%s", VLABISOImageFile),
				)
			}

			for idx := 0; idx < VLABPCIBridges; idx++ {
				args = append(args,
					"-device", fmt.Sprintf("i82801b11-bridge,id=dmi_pci_bridge%d", idx),
					"-device", fmt.Sprintf("pci-bridge,id=%s%d,bus=dmi_pci_bridge%d,chassis_nr=0x1,addr=0x%d,shpc=off", VLABPCIBridgePrefix, idx, idx, idx),
				)
			}

			for _, nic := range vm.NICs {
				args = append(args, strings.Split(nic, " ")...)
			}

			slog.Debug("Starting", "vm", vm.Name, "type", vm.Type, "cmd", VLABCmdQemuSystem+" "+strings.Join(args, " "))

			if err := execCmd(ctx, true, vmDir, VLABCmdQemuSystem, args, "vm", vm.Name); err != nil {
				slog.Warn("Failed running VM", "vm", vm.Name, "type", vm.Type, "err", err)

				if opts.CollectShowTech {
					if err := c.VLABShowTech(ctx, vlab); err != nil {
						slog.Warn("Failed to collect show-tech diagnostics", "err", err)

						return fmt.Errorf("getting show-tech: %w", err)
					}
				}

				if opts.FailFast {
					return fmt.Errorf("running vm: %w", err)
				}
			}

			return nil
		})

		if vm.Type == VMTypeServer || vm.Type == VMTypeControl || vm.Type == VMTypeGateway || vm.Type == VMTypeExternal {
			postProcesses.Add(1)
			group.Go(func() error {
				if err := c.vmPostProcess(ctx, vlab, d, vm, opts); err != nil {
					slog.Warn("Failed to post-process VM", "vm", vm.Name, "type", vm.Type, "err", err)

					if opts.CollectShowTech {
						if err := c.VLABShowTech(ctx, vlab); err != nil {
							slog.Warn("Failed to collect show-tech diagnostics", "err", err)

							return fmt.Errorf("getting show-tech: %w", err)
						}
					}

					if opts.FailFast {
						return fmt.Errorf("post-processing vm %s: %w", vm.Name, err)
					}
				}

				// no defer here, as we want to wait for all installers completion without errors
				postProcesses.Done()

				return nil
			})
		}
	}

	slog.Info("Starting VMs", "count", len(vlab.VMs), "cpu", fmt.Sprintf("%d vCPUs", cpu), "ram", fmt.Sprintf("%d MB", ram), "disk", fmt.Sprintf("%d GB", disk))

	group.Go(func() error {
		go func() {
			postProcesses.Wait()
			close(postProcessDone)
		}()

		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled: %w", ctx.Err())
		case <-postProcessDone:
		}

		slog.Info("All VMs are ready")

		if err := func() error {
			if len(opts.OnReady) > 0 {
				slog.Info("Running on-ready commands", "commands", opts.OnReady)
			}

			for _, cmd := range opts.OnReady {
				slog.Info("Running on-ready command", "command", cmd)
				switch OnReady(cmd) {
				case OnReadySwitchReinstall:
					if err := c.VLABSwitchReinstall(ctx, SwitchReinstallOpts{
						Mode:        ReinstallModeHardReset,
						PDUUsername: os.Getenv(VLABEnvPDUUsername),
						PDUPassword: os.Getenv(VLABEnvPDUPassword),
						WaitReady:   true,
					}); err != nil {
						slog.Warn("Failed to reinstall switches", "err", err)

						if opts.CollectShowTech {
							if err := c.VLABShowTech(ctx, vlab); err != nil {
								slog.Warn("Failed to collect show-tech diagnostics", "err", err)

								return fmt.Errorf("getting show-tech: %w", err)
							}
						}

						return fmt.Errorf("reinstalling switches: %w", err)
					}
				case OnReadySetupVPCs:
					// TODO make it configurable
					if err := c.SetupVPCs(ctx, vlab, SetupVPCsOpts{
						WaitSwitchesReady: true,
						VLANNamespace:     "default",
						IPv4Namespace:     "default",
						ServersPerSubnet:  1,
						SubnetsPerVPC:     2, // it makes it possible for some servers to have connectivity
						DNSServers:        []string{"1.1.1.1", "1.0.0.1"},
						TimeServers:       []string{"219.239.35.0"},
					}); err != nil {
						slog.Warn("Failed to setup VPCs", "err", err)

						if opts.CollectShowTech {
							if err := c.VLABShowTech(ctx, vlab); err != nil {
								slog.Warn("Failed to collect show-tech diagnostics", "err", err)

								return fmt.Errorf("getting show-tech: %w", err)
							}
						}

						return fmt.Errorf("setting up VPCs: %w", err)
					}
				case OnReadyTestConnectivity:
					// TODO make it configurable
					if err := c.TestConnectivity(ctx, vlab, TestConnectivityOpts{
						WaitSwitchesReady: true,
						PingsCount:        5,
						IPerfsSeconds:     5,
						CurlsCount:        3,
					}); err != nil {
						slog.Warn("Failed to test connectivity", "err", err)

						if opts.CollectShowTech {
							if err := c.VLABShowTech(ctx, vlab); err != nil {
								slog.Warn("Failed to collect show-tech diagnostics", "err", err)

								return fmt.Errorf("getting show-tech: %w", err)
							}
						}

						return fmt.Errorf("testing connectivity: %w", err)
					}
				case OnReadyExit:
					if opts.CollectShowTech {
						if err := c.VLABShowTech(ctx, vlab); err != nil {
							slog.Warn("Failed to collect show-tech diagnostics", "err", err)

							return fmt.Errorf("getting show-tech: %w", err)
						}
					}

					// TODO seems like some graceful shutdown logic isn't working in CI and we're getting stuck w/o this
					if os.Getenv("GITHUB_ACTIONS") == "true" {
						slog.Warn("Immediately exiting b/c running in GHA")
						os.Exit(0)
					}

					return ErrExit
				case OnReadyWait:
					if err := c.Wait(ctx, vlab); err != nil {
						slog.Warn("Failed to wait for switches ready", "err", err)

						if opts.CollectShowTech {
							if err := c.VLABShowTech(ctx, vlab); err != nil {
								slog.Warn("Failed to collect show-tech diagnostics", "err", err)

								return fmt.Errorf("getting show-tech: %w", err)
							}
						}

						return fmt.Errorf("waiting: %w", err)
					}
				case OnReadyInspect:
					if err := c.Inspect(ctx, vlab, InspectOpts{
						WaitAppliedFor: 2 * time.Minute,
						Strict:         !opts.ControlUpgrade,
					}); err != nil {
						slog.Warn("Failed to inspect", "err", err)

						if opts.CollectShowTech {
							if err := c.VLABShowTech(ctx, vlab); err != nil {
								slog.Warn("Failed to collect show-tech diagnostics", "err", err)

								return fmt.Errorf("getting show-tech: %w", err)
							}
						}

						return fmt.Errorf("inspecting: %w", err)
					}
				case OnReadyReleaseTest:
					if err := c.ReleaseTest(ctx, ReleaseTestOpts{ResultsFile: "release-test.xml"}); err != nil {
						slog.Warn("Failed to run release test", "err", err)

						return fmt.Errorf("release test: %w", err)
					}
				}
			}

			if len(opts.OnReady) > 0 {
				slog.Info("All on-ready commands finished")
			}

			return nil
		}(); err != nil {
			if errors.Is(err, ErrExit) {
				return err
			}

			slog.Warn("Error running on-ready commands", "err", err.Error())

			if opts.FailFast {
				return fmt.Errorf("running on-ready commands: %w", err)
			}
		}

		return nil
	})

	go func() {
		<-ctx.Done()
		time.Sleep(15 * time.Second)
		slog.Debug("Force exit with code 2", "err", ctx.Err())
		os.Exit(2)
	}()

	if err := group.Wait(); err != nil && !errors.Is(err, ErrExit) {
		return fmt.Errorf("running task: %w", err)
	}

	slog.Info("VLAB finished successfully")

	return nil
}

func isPresent(dir string, files ...string) bool {
	for _, file := range files {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			return false
		}
	}

	return true
}

func execCmd(ctx context.Context, sudo bool, baseDir, name string, args []string, logArgs ...any) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	aName := name
	if sudo {
		aName = VLABCmdSudo
		args = append([]string{name}, args...)
	}

	cmd := exec.CommandContext(ctx, aName, args...)
	cmd.Dir = baseDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, name+": ", logArgs...)
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, name+": ", logArgs...)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %q: %w", name, err)
	}

	return nil
}

func execHelper(ctx context.Context, baseDir string, args []string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}

	args = append([]string{self, "_helpers"}, args...)

	cmd := exec.CommandContext(ctx, VLABCmdSudo, args...)
	cmd.Dir = baseDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running helper %v: %w", args[1:], err)
	}

	return nil
}

func externalIgnition(fab fabapi.Fabricator, vm VM, extVRFMap map[string]ExternalVRFCfg) (string, []byte, error) {
	but, err := tmplutil.FromTemplate("butane", frrButaneTmpl, map[string]any{
		"Hostname":       vm.Name,
		"PasswordHash":   fab.Spec.Config.Control.DefaultUser.PasswordHash,
		"AuthorizedKeys": fab.Spec.Config.Control.DefaultUser.AuthorizedKeys,
		"ExternalVRFs":   extVRFMap,
	})
	if err != nil {
		return "", nil, fmt.Errorf("butane: %w", err)
	}

	ign, err := butaneutil.Translate(but)
	if err != nil {
		return but, nil, fmt.Errorf("translating butane: %w", err)
	}

	return but, ign, nil
}

func serverIgnition(fab fabapi.Fabricator, vm VM) ([]byte, error) {
	but, err := tmplutil.FromTemplate("butane", serverButaneTmpl, map[string]any{
		"Hostname":       vm.Name,
		"PasswordHash":   fab.Spec.Config.Control.DefaultUser.PasswordHash,
		"AuthorizedKeys": fab.Spec.Config.Control.DefaultUser.AuthorizedKeys,
	})
	if err != nil {
		return nil, fmt.Errorf("butane: %w", err)
	}

	ign, err := butaneutil.Translate(but)
	if err != nil {
		return nil, fmt.Errorf("translating butane: %w", err)
	}

	return ign, nil
}

func (c *Config) vmPostProcess(ctx context.Context, vlab *VLAB, d *artificer.Downloader, vm VM, opts VLABRunOpts) error {
	if vm.Type == VMTypeSwitch {
		return nil
	}

	slog.Debug("Waiting for VM to be ready", "vm", vm.Name, "type", vm.Type)

	timeout := 10 * time.Minute
	if vm.Type == VMTypeControl {
		timeout = 40 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	auth, err := goph.RawKey(vlab.SSHKey, "")
	if err != nil {
		return fmt.Errorf("getting ssh auth: %w", err)
	}

	slog.Debug("Waiting for ssh", "vm", vm.Name, "type", vm.Type)

	var client *goph.Client
	ready := false
	for !ready {
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled: %w", ctx.Err())
		case <-time.After(5 * time.Second):
			client, err = goph.NewConn(&goph.Config{
				User:     "core",
				Addr:     "127.0.0.1",
				Port:     getSSHPort(vm.ID),
				Auth:     auth,
				Timeout:  10 * time.Second,
				Callback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
			})
			if err != nil {
				// if !errors.Is(err, syscall.ECONNREFUSED) {
				// 	slog.Debug("SSH not ready yet", "vm", vm.Name, "type", vm.Type, "err", err)
				// }

				continue
			}
			defer client.Close()

			ready = true
		}
	}

	out, err := client.RunContext(ctx, "hostname")
	if err != nil {
		return fmt.Errorf("checking hostname: %w", err)
	}

	hostname := strings.TrimSpace(string(out))
	if hostname != vm.Name {
		return fmt.Errorf("hostname mismatch: got %q, want %q", hostname, vm.Name) //nolint:goerr113
	}

	sftp, err := client.NewSftp()
	if err != nil {
		return fmt.Errorf("creating sftp: %w", err)
	}
	defer sftp.Close()

	slog.Debug("SSH is ready", "vm", vm.Name, "type", vm.Type)

	if vm.Type == VMTypeServer {
		slog.Debug("Installing helpers", "vm", vm.Name, "type", vm.Type)

		f, err := sftp.Create("/tmp/hhnet")
		if err != nil {
			return fmt.Errorf("creating hhnet: %w", err)
		}
		defer f.Close()

		if _, err := f.Write(hhnet); err != nil {
			return fmt.Errorf("writing hhnet: %w", err)
		}

		if _, err := client.RunContext(ctx, "bash -c 'sudo mv /tmp/hhnet /opt/bin/hhnet && chmod +x /opt/bin/hhnet'"); err != nil {
			return fmt.Errorf("installing hhnet: %w", err)
		}

		if err := d.WithORAS(ctx, flatcar.ToolboxRef, flatcar.ToolboxVersion(c.Fab), func(cachePath string) error {
			if err := client.Upload(filepath.Join(cachePath, "toolbox.tar"), "/tmp/toolbox"); err != nil {
				return fmt.Errorf("uploading: %w", err)
			}

			return nil
		}); err != nil {
			return fmt.Errorf("uploading toolbox image: %w", err)
		}

		if _, err := client.RunContext(ctx, "bash -c 'sudo ctr image import /tmp/toolbox'"); err != nil {
			return fmt.Errorf("installing toolbox: %w", err)
		}

		if err := sftp.Remove("/tmp/toolbox"); err != nil {
			return fmt.Errorf("removing toolbox image: %w", err)
		}

		if _, err := client.RunContext(ctx, "bash -c 'toolbox hostname'"); err != nil {
			return fmt.Errorf("trying toolbox: %w", err)
		}
	} else if vm.Type == VMTypeControl {
		if opts.BuildMode == recipe.BuildModeManual || opts.ControlUpgrade {
			marker, err := sshReadMarker(sftp)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("checking for install marker: %w", err)
			}
			if err == nil && marker != recipe.InstallMarkerComplete {
				slog.Error("Control node install was already attempted but not completed", "vm", vm.Name, "type", vm.Type, "marker", marker)

				return fmt.Errorf("not complete install marker: %q", marker) //nolint:goerr113
			}
			if err == nil && !opts.ControlUpgrade && marker == recipe.InstallMarkerComplete {
				slog.Info("Control node install was already completed", "vm", vm.Name, "type", vm.Type)
			} else {
				slog.Info("Uploading control install", "vm", vm.Name, "type", vm.Type)

				if out, err := client.RunContext(ctx, fmt.Sprintf("bash -c 'rm -rf %s*'", vm.Name)); err != nil {
					return fmt.Errorf("removing previous control install: %w: %s", err, string(out))
				}

				installArchive := vm.Name + recipe.InstallArchiveSuffix
				local := filepath.Join(c.WorkDir, ResultDir, installArchive)
				remote := filepath.Join(flatcar.Home, installArchive)
				if err := client.Upload(local, remote); err != nil {
					return fmt.Errorf("uploading control install: %w", err)
				}

				if out, err := client.RunContext(ctx, fmt.Sprintf("bash -c 'tar xzf %s'", remote)); err != nil {
					return fmt.Errorf("extracting control install: %w: %s", err, string(out))
				}

				mode := "install"
				if opts.ControlUpgrade {
					mode = "upgrade"
				}

				slog.Info("Running control "+mode, "vm", vm.Name, "type", vm.Type)
				installCmd := fmt.Sprintf("cd %s && sudo ./%s control "+mode, vm.Name+recipe.InstallSuffix, recipe.RecipeBin)
				if slog.Default().Enabled(ctx, slog.LevelDebug) {
					installCmd += " -v"
				}
				if err := sshExec(ctx, vm, client, installCmd, "control-"+mode, slog.Info); err != nil {
					return fmt.Errorf("running control %s: %w", mode, err)
				}
				slog.Info("Control "+mode+" completed", "vm", vm.Name, "type", vm.Type)
			}
		} else {
			slog.Debug("Waiting for control node to be auto installed (via USB)", "vm", vm.Name, "type", vm.Type)

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			if slog.Default().Enabled(ctx, slog.LevelInfo) {
				go func() {
					if err := sshExec(ctx, vm, client, "journalctl -n 100 -fu fabric-install.service", "fabric-install", slog.Info); err != nil {
						slog.Info("Journalctl on control node failed", "vm", vm.Name, "type", vm.Type, "err", err)
					}
				}()
			}

			installed := false
			for !installed {
				select {
				case <-ctx.Done():
					return fmt.Errorf("cancelled: %w", ctx.Err())
				case <-time.After(5 * time.Second):
					marker, err := sshReadMarker(sftp)
					if err != nil {
						if errors.Is(err, os.ErrNotExist) {
							continue
						}

						return err
					}

					if marker != recipe.InstallMarkerComplete {
						return fmt.Errorf("not complete install marker: %q", marker) //nolint:goerr113
					} else {
						installed = true
					}
				}
			}
		}

		slog.Debug("Control node install marker is complete", "vm", vm.Name, "type", vm.Type)

		kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
		if err := client.Download(k3s.KubeConfigPath, kubeconfig); err != nil {
			return fmt.Errorf("downloading kubeconfig: %w", err)
		}
		slog.Debug("Control node kubeconfig is downloaded", "path", kubeconfig, "vm", vm.Name, "type", vm.Type)

		slog.Info("Waiting for K8s API to be ready", "vm", vm.Name, "type", vm.Type)
		api := false
		var apiErr error
		for !api {
			if apiErr != nil {
				select {
				case <-ctx.Done():
					slog.Error("Failed to wait for k8s api", "vm", vm.Name, "type", vm.Type, "err", apiErr)

					return fmt.Errorf("cancelled while waiting for k8s api: %w", ctx.Err())
				case <-time.After(5 * time.Second):
				}
			}

			kube, err := kubeutil.NewClient(ctx, kubeconfig, fabapi.SchemeBuilder)
			if err != nil {
				apiErr = err
				slog.Debug("Failed to create kube client", "err", err)

				continue
			}

			fabs := &fabapi.FabricatorList{}
			if err := kube.List(ctx, fabs); err != nil {
				apiErr = err
				slog.Debug("Failed to list fabricator configs", "vm", vm.Name, "type", vm.Type, "err", err)

				continue
			}

			if len(fabs.Items) == 0 {
				apiErr = fmt.Errorf("no fabricator configs found") //nolint:goerr113
				slog.Debug("No fabricator configs found", "vm", vm.Name, "type", vm.Type)

				continue
			}

			if len(fabs.Items) > 1 {
				return fmt.Errorf("multiple fabricator configs found") //nolint:goerr113
			}

			if fabs.Items[0].Name != comp.FabName || fabs.Items[0].Namespace != comp.FabNamespace {
				return fmt.Errorf("fabricator config mismatch: got %s/%s, want %s/%s", fabs.Items[0].Namespace, fabs.Items[0].Name, comp.FabNamespace, comp.FabName) //nolint:goerr113
			}

			apiErr = nil
			api = true

			slog.Debug("K8s API is ready", "vm", vm.Name, "type", vm.Type)
		}

		slog.Info("Control node is ready", "vm", vm.Name, "type", vm.Type)
	}

	slog.Debug("VM is ready", "vm", vm.Name, "type", vm.Type)

	return nil
}

func sshExec(ctx context.Context, vm VM, client *goph.Client, exec, name string, log func(msg string, args ...any)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd, err := client.CommandContext(ctx, "bash", "-c", "'"+exec+"'")
	if err != nil {
		return fmt.Errorf("creating ssh command %q: %w", exec, err)
	}

	cmd.Stdout = logutil.NewSink(ctx, log, name+": ", "vm", vm.Name)
	cmd.Stderr = logutil.NewSink(ctx, log, name+": ", "vm", vm.Name)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running ssh command %q: %w", exec, err)
	}

	return nil
}

func sshReadMarker(sftp *sftp.Client) (string, error) {
	f, err := sftp.Open(recipe.InstallMarkerFile)
	if err != nil {
		return "", fmt.Errorf("checking for install marker: %w", err)
	}

	rawMarker, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("reading install marker: %w", err)
	}

	return strings.TrimSpace(string(rawMarker)), nil
}
