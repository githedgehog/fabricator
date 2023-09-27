package vlab

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"golang.org/x/sync/errgroup"
)

var SSH_QUIET_FLAGS = []string{
	"-o", "GlobalKnownHostsFile=/dev/null",
	"-o", "UserKnownHostsFile=/dev/null",
	"-o", "StrictHostKeyChecking=no",
	"-o", "LogLevel=ERROR",
}

//go:embed onie-eeprom.tmpl.yaml
var onieEepromConfigTmpl string

type VMOS string

const (
	VMOS_ONIE    VMOS = "onie"
	VMOS_FLATCAR VMOS = "flatcar"
)

type VM struct {
	Basedir   string
	ID        int
	Name      string
	OS        VMOS
	IsControl bool

	Links   []*Link
	SSHPort int

	Cfg *Config

	TPMReady chan any // TODO

	Ready     fileMarker
	Installed fileMarker
}

type Link struct {
	DevID string
	MAC   string

	LocalIfPort   int
	LocalPortName string
	DestName      string
	DestIfPort    int
	DestPortName  string

	// Only for control node for now
	IsHostFwd    bool
	SSHPort      int
	KubePort     int
	RegistryPort int
}

func (vm *VM) UUID() string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", vm.ID)
}

func (vm *VM) Run(ctx context.Context, eg *errgroup.Group) {
	vm.TPMReady = make(chan any)

	eg.Go(vm.RunTPM(ctx))
	eg.Go(vm.RunVM(ctx))
	eg.Go(vm.RunInstall(ctx))
}

func (vm *VM) RunVM(ctx context.Context) func() error {
	return func() error {
		// <-vm.TPMReady
		time.Sleep(3 * time.Second) // TODO please, no!

		slog.Info("Running VM", "id", vm.ID, "name", vm.Name, "os", vm.OS, "control", vm.IsControl)

		// This is an ugly workaround in a bug in swtpm:
		// If you specify both --server and --ctrl flags for the socket swtpm,
		// then it exits if you start with QEMU directly. If you run a command,
		// then it will continue to work.
		err := execCmd(ctx, vm.Basedir, true, "tpm2", []string{"TPM2TOOLS_TCTI=swtpm:path=tpm.sock"}, "startup")
		if err != nil {
			return errors.Wrapf(err, "error starting tpm")
		}

		// total for default vlab (control + 2 x switch + 2 x server)
		// default: 16 vCPU 20.480 GB RAM
		// compact: 14 vCPU 13.312 GB RAM

		cpu := "2"
		memory := "1024"
		if vm.OS == VMOS_ONIE {
			cpu = "4"
			memory = "5120"
		}
		if vm.IsControl {
			cpu = "4"
			memory = "8192" // TODO cut down to 6144?
		}
		if vm.Cfg.Compact {
			cpu = "1"
			memory = "512"
			if vm.OS == VMOS_ONIE {
				cpu = "4"
				memory = "3584"
			}
			if vm.IsControl {
				cpu = "4"
				memory = "5120"
			}
		}

		args := []string{
			"-name", vm.Name,
			"-uuid", vm.UUID(),
			"-m", memory,
			"-machine", "q35,accel=kvm,smm=on", "-cpu", "host", "-smp", cpu,
			"-object", "rng-random,filename=/dev/urandom,id=rng0", "-device", "virtio-rng-pci,rng=rng0",
			"-chardev", "socket,id=chrtpm,path=tpm.sock.ctrl", "-tpmdev", "emulator,id=tpm0,chardev=chrtpm", "-device", "tpm-tis,tpmdev=tpm0",
			"-drive", "if=virtio,file=os.img",
			"-drive", "if=pflash,file=efi_code.fd,format=raw,readonly=on",
			"-drive", "if=pflash,file=efi_vars.fd,format=raw",
			"-display", "none",
			"-vga", "none",
			"-serial", "unix:serial.sock,server,nowait",
			"-monitor", "unix:monitor.sock,server,nowait",
			"-qmp", "unix:qmp.sock,server,nowait",
			"-global", "ICH9-LPC.disable_s3=1",
		}

		if vm.OS == VMOS_FLATCAR {
			args = append(args,
				"-fw_cfg", "name=opt/org.flatcar-linux/config,file=ignition.json",
			)
		} else if vm.OS == VMOS_ONIE {
			args = append(args,
				// TODO why it's needed and should we apply it to all VMs?
				"-device", "pcie-root-port,bus=pcie.0,id=rp1,slot=1", "-device", "pcie-pci-bridge,id=br1,bus=rp1",
			)
		}

		for _, link := range vm.Links {
			if link.IsHostFwd {
				args = append(args,
					// TODO optionally make control node isolated using ",restrict=yes"
					"-netdev", fmt.Sprintf("user,id=%s,hostfwd=tcp:127.0.0.1:%d-:22,hostfwd=tcp:127.0.0.1:%d-:6443,hostfwd=tcp:127.0.0.1:%d-:31000,hostname=%s,domainname=local,dnssearch=local,net=10.100.0.0/24,dhcpstart=10.100.0.10",
						link.DevID, link.SSHPort, link.KubePort, link.RegistryPort, vm.Name),
					"-device", fmt.Sprintf("virtio-net-pci,netdev=%s,mac=%s", link.DevID, link.MAC),
				)
			} else {
				args = append(args,
					"-netdev", fmt.Sprintf("socket,id=%s,udp=127.0.0.1:%d,localaddr=127.0.0.1:%d", link.DevID, link.LocalIfPort, link.DestIfPort),
					"-device", fmt.Sprintf("virtio-net-pci,netdev=%s,mac=%s", link.DevID, link.MAC),
				)
			}
		}

		if vm.IsControl && vm.SSHPort == 0 {
			return errors.Errorf("error running control vm: no ssh port found")
		}

		if vm.Cfg.DryRun {
			return nil
		}

		return errors.Wrapf(execCmd(ctx, vm.Basedir, true, "qemu-system-x86_64", []string{}, args...), "error running vm")
	}
}

func (vm *VM) RunInstall(ctx context.Context) func() error {
	run := func(ctx context.Context) error {
		if !vm.IsControl {
			return nil
		}

		if vm.Cfg.DryRun {
			return nil
		}

		if vm.Installed.Is() {
			slog.Debug("Control node is already installed", "name", vm.Name)
			return nil
		}

		slog.Info("Installing control node")

		ctx, cancel := context.WithTimeoutCause(ctx, 10*time.Minute, errors.New("controller installation timed out")) // TODO
		defer cancel()

		slog.Info("Waiting for control node ssh")

		ticker := time.NewTicker(5 * time.Second) // TODO
		defer ticker.Stop()

	loop: // oops, some goto :)
		for {
			select {
			case <-ticker.C:
				err := vm.ssh(ctx, true, "hostname")
				if err != nil {
					// just waiting
					slog.Debug("Can't ssh to control node", "error", err)
				} else {
					break loop
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		slog.Info("Control node ssh is available")

		// TODO k3s really don't like when we don't have default route
		// err := vm.ssh(ctx, "sudo ip route add default via 10.100.0.2 dev eth0")
		// if err != nil {
		// 	return errors.Wrap(err, "error setting default route")
		// }

		slog.Info("Uploading installer")
		err := vm.upload(ctx, false, vm.Cfg.ControlInstaller+".tgz", "~/")
		if err != nil {
			return errors.Wrap(err, "error uploading installer")
		}
		slog.Debug("Installer uploaded")

		slog.Info("Running installer on control node")
		installCmd := "tar xzf control-install.tgz && cd control-install && sudo ./hhfab-recipe run"
		if slog.Default().Enabled(ctx, slog.LevelDebug) {
			installCmd += " -v"
		}
		err = vm.ssh(ctx, false, installCmd)
		if err != nil {
			return errors.Wrap(err, "error installing control node")
		}

		err = vm.download(ctx, true, "/etc/rancher/k3s/k3s.yaml", filepath.Join(vm.Cfg.Basedir, "kubeconfig.yaml"))
		if err != nil {
			return errors.Wrapf(err, "error downloading kubeconfig")
		}

		slog.Info("Control node installed")

		err = vm.Installed.Mark()
		if err != nil {
			return errors.Wrapf(err, "error marking control node as installed")
		}

		return nil
	}

	return func() error {
		err := run(ctx)
		if err != nil {
			slog.Error("Error installing control node", "error", err)
		}

		return nil
	}
}

func (vm *VM) RunTPM(ctx context.Context) func() error {
	return func() error {
		err := execCmd(ctx, vm.Basedir, true, "swtpm", []string{}, "socket", "--tpm2", "--tpmstate", "dir=tpm",
			"--ctrl", "type=unixio,path=tpm.sock.ctrl", "--server", "type=unixio,path=tpm.sock", "--pid", "file=tpm.pid",
			"--log", "level=1", "--flags", "startup-clear")
		if err != nil {
			return errors.Wrapf(err, "error starting tpm")
		}

		// TODO start command first and wait for socket to appear, than report TPM is ready
		// close(vm.TPMReady)

		return nil
	}
}

func (vm *VM) Prepare(ctx context.Context, cfg *Config) error {
	if vm.OS == VMOS_ONIE && vm.IsControl {
		return errors.New("onie could not be used as control node")
	}
	if vm.OS != VMOS_ONIE && vm.OS != VMOS_FLATCAR {
		return errors.New("unsupported OS")
	}

	if vm.Ready.Is() {
		slog.Debug("VM is already prepared", "name", vm.Name)
		return nil
	}

	slog.Info("Preparing VM", "id", vm.ID, "name", vm.Name, "os", vm.OS, "control", vm.IsControl)

	err := os.MkdirAll(vm.Basedir, 0o755)
	if err != nil {
		return errors.Wrapf(err, "error creating vm basedir")
	}

	files := map[string]string{}
	if vm.OS == VMOS_FLATCAR {
		files["os.img"] = filepath.Join(cfg.FilesDir, "flatcar.img")
		files["efi_code.fd"] = filepath.Join(cfg.FilesDir, "flatcar_efi_code.fd")
		files["efi_vars.fd"] = filepath.Join(cfg.FilesDir, "flatcar_efi_vars.fd")

		if vm.IsControl {
			files["ignition.json"] = cfg.ControlIgnition
		} else {
			files["ignition.json"] = filepath.Join(cfg.ServerIgnitionDir, fmt.Sprintf("%s.ignition.json", vm.Name))
		}
	}
	if vm.OS == VMOS_ONIE {
		files["os.img"] = filepath.Join(cfg.FilesDir, "onie-kvm_x86_64.qcow2")
		files["efi_code.fd"] = filepath.Join(cfg.FilesDir, "onie_efi_code.fd")
		files["efi_vars.fd"] = filepath.Join(cfg.FilesDir, "onie_efi_vars.fd")
	}

	err = vm.copyFiles(ctx, cfg, files)
	if err != nil {
		return errors.Wrapf(err, "error copying files")
	}

	slog.Info("Resizing VM image (may require sudo password)", "name", vm.Name)

	err = execCmd(ctx, vm.Basedir, true, "qemu-img", []string{}, "resize", "os.img", "50G")
	if err != nil {
		return errors.Wrapf(err, "error resizing image")
	}

	if vm.OS == VMOS_ONIE {
		err = os.WriteFile(filepath.Join(vm.Basedir, "onie-eeprom.yaml"),
			[]byte(fmt.Sprintf(onieEepromConfigTmpl,
				vm.Name,
				uuid.New().String(),
				vm.ID,
				time.Now().Format(time.DateTime),
				len(vm.Links))),
			0o644)
		if err != nil {
			return errors.Wrapf(err, "error writing onie-eeprom.yaml")
		}

		slog.Info("Writing ONIE EEPROM (may require sudo password)", "name", vm.Name)

		err = execCmd(ctx, "", true, "sudo", []string{}, filepath.Join(vm.Cfg.FilesDir, "onie-qcow2-eeprom-edit"),
			"--log-level=debug", "write", "--force", "--input", filepath.Join(vm.Basedir, "onie-eeprom.yaml"), filepath.Join(vm.Basedir, "os.img"))
		if err != nil {
			return errors.Wrapf(err, "error writing onie-eeprom.yaml")
		}
	}

	err = os.MkdirAll(filepath.Join(vm.Basedir, "tpm"), 0o755)
	if err != nil {
		return errors.Wrapf(err, "error creating tpm dir")
	}

	slog.Info("Initializing TPM", "name", vm.Name)

	err = execCmd(ctx, vm.Basedir, true, "swtpm_setup", []string{},
		"--tpm2", "--tpmstate", "tpm", "--createek", "--decryption", "--create-ek-cert", "--create-platform-cert",
		"--create-spk", "--vmid", vm.Name, "--overwrite", "--display")
	if err != nil {
		return errors.Wrapf(err, "error initializing tpm")
	}

	err = vm.Ready.Mark()
	if err != nil {
		return errors.Wrapf(err, "error marking vm as ready")
	}

	slog.Debug("VM prepared", "name", vm.Name)

	return nil
}

func (vm *VM) copyFiles(ctx context.Context, cfg *Config, names map[string]string) error {
	for toName, from := range names {
		to := filepath.Join(vm.Basedir, toName)

		slog.Info("Copying files ", "from", from, "to", to)

		fromFile, err := os.Open(from)
		if err != nil {
			return errors.Wrapf(err, "error opening file %s", from)
		}
		defer fromFile.Close()

		toFile, err := os.Create(to)
		if err != nil {
			return errors.Wrapf(err, "error creating file %s", to)
		}
		defer toFile.Close()

		p := mpb.New(
			mpb.WithWidth(60),
		)

		info, err := fromFile.Stat()
		if err != nil {
			return errors.Wrapf(err, "error getting file info %s", from)
		}

		var reader io.ReadCloser = fromFile

		if slog.Default().Enabled(ctx, slog.LevelInfo) && info.Size() > 10_000_000 {
			bar := p.AddBar(info.Size(),
				mpb.PrependDecorators(
					decor.Counters(decor.SizeB1024(0), "% .2f / % .2f", decor.WCSyncSpace),
				),
				mpb.AppendDecorators(
					decor.EwmaSpeed(decor.SizeB1024(0), "% .2f", 30),
					decor.OnComplete(
						decor.EwmaETA(decor.ET_STYLE_GO, 30, decor.WCSyncSpace), "done",
					),
				),
			)

			reader = bar.ProxyReader(fromFile)
			defer reader.Close()
		}

		_, err = io.Copy(toFile, reader)
		if err != nil {
			return errors.Wrapf(err, "error copying file %s to %s", from, to)
		}

		p.Wait()
	}

	return nil
}

func (vm *VM) ssh(ctx context.Context, quiet bool, command string) error {
	args := append(SSH_QUIET_FLAGS,
		"-p", fmt.Sprintf("%d", vm.SSHPort),
		"-i", vm.Cfg.SshKey,
		"core@127.0.0.1",
		command,
	)

	return execCmd(ctx, "", quiet, "ssh", []string{}, args...)
}

func (vm *VM) upload(ctx context.Context, quiet bool, from, to string) error {
	args := append(SSH_QUIET_FLAGS,
		"-P", fmt.Sprintf("%d", vm.SSHPort),
		"-i", vm.Cfg.SshKey,
		"-r",
		from,
		"core@127.0.0.1:"+to,
	)

	return execCmd(ctx, "", quiet, "scp", []string{}, args...)
}

func (vm *VM) download(ctx context.Context, quiet bool, from, to string) error {
	args := append(SSH_QUIET_FLAGS,
		"-P", fmt.Sprintf("%d", vm.SSHPort),
		"-i", vm.Cfg.SshKey,
		"-r",
		"core@127.0.0.1:"+from,
		to,
	)

	return execCmd(ctx, "", quiet, "scp", []string{}, args...)
}

func execCmd(ctx context.Context, basedir string, quiet bool, name string, env []string, args ...string) error {
	argsStr := strings.Join(args, " ")
	argsStr = strings.ReplaceAll(argsStr, strings.Join(SSH_QUIET_FLAGS, " "), "")

	slog.Debug("Running command", "name", name, "args", argsStr)

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = basedir
	cmd.Env = append(os.Environ(), env...)

	logFileName := filepath.Join(basedir, "exec.log")
	logFile, err := os.OpenFile(logFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return errors.Wrapf(err, "error opening log file %s", logFileName)
	}
	defer logFile.Close()

	outputs := []io.Writer{logFile}

	if !quiet {
		outputs = append(outputs, os.Stdout)
	}

	cmd.Stdout = io.MultiWriter(outputs...)
	cmd.Stderr = io.MultiWriter(outputs...)

	// TODO save logs to files?
	// stdout, err := cmd.StdoutPipe()
	// if err != nil {
	// 	return err
	// }

	// stderr, err := cmd.StderrPipe()
	// if err != nil {
	// 	return err
	// }

	// stdoutS := bufio.NewScanner(stdout)
	// stderrS := bufio.NewScanner(stderr)

	// go func() {
	// 	for stdoutS.Scan() {
	// 		slog.Info("RUNNING STDOUT " + name + ":  " + stdoutS.Text()) // write each line to your log, or anything you need
	// 	}
	// 	if err := stdoutS.Err(); err != nil {
	// 		log.Printf("error: %s", err)
	// 	}
	// }()

	// go func() {
	// 	for stderrS.Scan() {
	// 		slog.Info("RUNNING STDERR " + name + ":  " + stderrS.Text()) // write each line to your log, or anything you need
	// 	}
	// 	if err := stderrS.Err(); err != nil {
	// 		log.Printf("error: %s", err)
	// 	}
	// }()

	return errors.Wrapf(cmd.Run(), "error running command %s %s", name, argsStr)
}
