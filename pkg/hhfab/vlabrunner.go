package hhfab

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/util/butaneutil"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"golang.org/x/sync/errgroup"
)

//go:embed vlab_butane.tmpl.yaml
var serverButaneTmpl string

const (
	VLABOSImageFile = "os.img"
	VLABEFICodeFile = "efi_code.fd"
	VLABEFIVarsFile = "efi_vars.fd"

	VLABSerialLog  = "serial.log"
	VLABSerialSock = "serial.sock"
	VLABMonSock    = "mon.sock"
	VLABQMPSock    = "qmp.sock"

	VLABCmdSudo       = "sudo"
	VLABCmdQemuImg    = "qemu-img"
	VLABCmdQemuSystem = "qemu-system-x86_64"

	VLABIgnition = "ignition.json"
)

var VLABCmds = []string{VLABCmdSudo, VLABCmdQemuImg, VLABCmdQemuSystem}

type VLABRunOpts struct {
	KillStale          bool
	ControlsRestricted bool
	ServersRestricted  bool
}

func (c *Config) VLABRun(ctx context.Context, vlab *VLAB, opts VLABRunOpts) error {
	for _, cmd := range VLABCmds {
		_, err := exec.LookPath(cmd)
		if err != nil {
			return fmt.Errorf("required command %q is not available", cmd) //nolint:goerr113
		}
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
		if err := execHelper(ctx, c.WorkDir, append([]string{"bind-devices"}, toBind...)); err != nil {
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
		vmDir := filepath.Join(c.WorkDir, VLABDir, VLABVMsDir, vm.Name)

		if isPresent(vmDir, VLABOSImageFile, VLABEFICodeFile, VLABEFIVarsFile) {
			slog.Info("Using existing", "vm", vm.Name, "type", vm.Type)
		} else {
			slog.Info("Preparing new", "vm", vm.Name, "type", vm.Type)

			if err := os.MkdirAll(vmDir, 0o700); err != nil {
				return fmt.Errorf("creating VM dir %q: %w", vmDir, err)
			}

			if vm.Type == VMTypeControl || vm.Type == VMTypeServer {
				if err := d.FromORAS(ctx, vmDir, "fabricator/flatcar-vlab", "v3975.2.1", []artificer.ORASFile{
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
			} else if vm.Type == VMTypeSwitch {
				if err := d.FromORAS(ctx, vmDir, "fabricator/onie-vlab", "test3", []artificer.ORASFile{
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

			if err := execCmd(ctx, false, vmDir,
				VLABCmdQemuImg, []string{"resize", VLABOSImageFile, fmt.Sprintf("%dG", vm.Size.Disk)},
				"vm", vm.Name); err != nil {
				return fmt.Errorf("resizing os image: %w", err)
			}

			if vm.Type == VMTypeServer {
				ign, err := serverIgnition(c.Fab, vm)
				if err != nil {
					return fmt.Errorf("generating ignition: %w", err)
				}

				if err := os.WriteFile(filepath.Join(vmDir, VLABIgnition), ign, 0o600); err != nil {
					return fmt.Errorf("writing ignition: %w", err)
				}
			}
		}
	}

	group := &errgroup.Group{}
	installers := &sync.WaitGroup{}

	for _, vm := range vlab.VMs {
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
				"-drive", "if=virtio,file=os.img,index=0",
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

			if vm.Type == VMTypeControl || vm.Type == VMTypeServer {
				ign := VLABIgnition
				if vm.Type == VMTypeControl {
					ign = filepath.Join(c.WorkDir, ResultDir, vm.Name+recipe.InstallIgnitionSuffix)
				}
				args = append(args,
					"-fw_cfg", "name=opt/org.flatcar-linux/config,file="+ign,
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
				return fmt.Errorf("running vm: %w", err)
			}

			return nil
		})

		if vm.Type == VMTypeControl || vm.Type == VMTypeServer {
			installers.Add(1)
			group.Go(func() error {
				// TODO install VM
				// TODO some flag to control "fail-on-install" behavior

				time.Sleep(5 * time.Second)

				// no defer here, as we want to wait for all installers completion without errors
				installers.Done()

				return nil
			})
		}
	}

	slog.Info("Starting VMs", "total", len(vlab.VMs), "cpu", fmt.Sprintf("%d vCPUs", cpu), "ram", fmt.Sprintf("%d MB", ram), "disk", fmt.Sprintf("%d GB", disk))

	group.Go(func() error {
		installers.Wait()

		slog.Info("All VM installers completed")

		return nil
	})

	if err := group.Wait(); err != nil {
		return fmt.Errorf("running task: %w", err)
	}

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
