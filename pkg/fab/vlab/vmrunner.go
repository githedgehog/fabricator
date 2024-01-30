package vlab

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

const (
	IFACES_PER_PCI_BRIDGE = 32
)

func (vm *VM) Run(ctx context.Context, eg *errgroup.Group, svc *Service) {
	svcCfg := svc.cfg

	if vm.Type == VMTypeControl || vm.Type == VMTypeSwitchVS {
		eg.Go(vm.RunTPM(ctx, svcCfg))
	}

	eg.Go(vm.RunVM(ctx, svcCfg))

	eg.Go(vm.RunInstall(ctx, svc))
}

func (svcCfg *ServiceConfig) tpmSudo() string {
	if svcCfg.SudoSwtpm {
		return "sudo"
	} else {
		return "time"
	}
}

func (vm *VM) RunTPM(ctx context.Context, svcCfg *ServiceConfig) func() error {
	return func() error {
		slog.Info("Running swtpm (may require sudo password)", "name", vm.Name)

		err := execCmd(ctx, svcCfg, vm.Basedir, true, svcCfg.tpmSudo(), []string{},
			"swtpm", "socket", "--tpm2", "--tpmstate", "dir=tpm",
			"--ctrl", "type=unixio,path=tpm.sock.ctrl", "--pid", "file=tpm.pid",
			"--log", "level=1", "--flags", "startup-clear")
		if err != nil {
			return errors.Wrapf(err, "error starting tpm")
		}

		// TODO start command first and wait for socket to appear, than report TPM is ready
		// close(vm.TPMReady)

		return nil
	}
}

func (vm *VM) RunVM(ctx context.Context, svcCfg *ServiceConfig) func() error {
	return func() error {
		// <-vm.TPMReady
		time.Sleep(3 * time.Second) // TODO please, no!

		slog.Info("Running VM", "id", vm.ID, "name", vm.Name, "type", vm.Type)

		args := []string{
			"qemu-system-x86_64",
			"-name", vm.Name,
			"-uuid", vm.UUID(),
			"-m", fmt.Sprintf("%dM", vm.Config.RAM),
			"-machine", "q35,accel=kvm,smm=on", "-cpu", "host", "-smp", fmt.Sprintf("%d", vm.Config.CPU),
			"-object", "rng-random,filename=/dev/urandom,id=rng0", "-device", "virtio-rng-pci,rng=rng0",
			"-drive", "if=virtio,file=os.img,index=0",
			"-drive", "if=pflash,file=efi_code.fd,format=raw,readonly=on",
			"-drive", "if=pflash,file=efi_vars.fd,format=raw",
			"-display", "none",
			"-vga", "none",
			"-serial", "unix:serial.sock,server,nowait",
			"-monitor", "unix:monitor.sock,server,nowait",
			"-qmp", "unix:qmp.sock,server,nowait",
			"-global", "ICH9-LPC.disable_s3=1",
		}

		usbfi, _ := os.Stat(filepath.Join(vm.Basedir, "usb.img"))
		if usbfi != nil {
			args = append(args,
				"-drive", "if=virtio,file=usb.img,index=1",
			)
		}

		if vm.Type == VMTypeControl || vm.Type == VMTypeSwitchVS {
			args = append(args,
				"-chardev", "socket,id=chrtpm,path=tpm.sock.ctrl",
				"-tpmdev", "emulator,id=tpm0,chardev=chrtpm",
				"-device", "tpm-tis,tpmdev=tpm0",
			)
		}

		if vm.Type == VMTypeControl || vm.Type == VMTypeServer {
			args = append(args,
				"-fw_cfg", "name=opt/org.flatcar-linux/config,file=ignition.json",
			)
		}

		for idx := 0; idx <= len(vm.Interfaces)/IFACES_PER_PCI_BRIDGE; idx++ {
			args = append(args,
				"-device", fmt.Sprintf("i82801b11-bridge,id=dmi_pci_bridge%d", idx),
				"-device", fmt.Sprintf("pci-bridge,id=pci-bridge%d,bus=dmi_pci_bridge%d,chassis_nr=0x1,addr=0x%d,shpc=off", idx, idx, idx),
			)
		}

		for ifaceID := 0; ifaceID < len(vm.Interfaces); ifaceID++ {
			iface := vm.Interfaces[ifaceID]
			deviceID := fmt.Sprintf("eth%02d", ifaceID)

			device := ""
			netdev := ""

			if iface.Passthrough != "" {
				device = fmt.Sprintf("vfio-pci,host=%s,id=%s", iface.Passthrough, deviceID)
			} else {
				device = fmt.Sprintf("e1000,mac=%s", vm.macFor(ifaceID))
				if iface.Netdev != "" {
					device += fmt.Sprintf(",netdev=%s", deviceID)
					netdev = fmt.Sprintf("%s,id=%s", iface.Netdev, deviceID)
				}
			}

			device += fmt.Sprintf(",bus=pci-bridge%d,addr=0x%x", ifaceID/IFACES_PER_PCI_BRIDGE, ifaceID%IFACES_PER_PCI_BRIDGE)

			if netdev != "" {
				args = append(args, "-netdev", netdev)
			}
			args = append(args, "-device", device)
		}

		return errors.Wrapf(execCmd(ctx, svcCfg, vm.Basedir, true, "sudo", []string{}, args...), "error running vm")
	}
}

func (vm *VM) RunInstall(ctx context.Context, svc *Service) func() error {
	run := func(ctx context.Context) error {
		if vm.Type != VMTypeControl && vm.Type != VMTypeServer {
			return nil
		}

		if vm.Installed.Is() {
			slog.Debug("VM is already installed", "name", vm.Name)
			return nil
		}

		svcCfg := svc.cfg
		if svcCfg.DryRun {
			return nil
		}

		slog.Info("Installing VM", "name", vm.Name, "type", vm.Type)

		ctx, cancel := context.WithTimeoutCause(ctx, 10*time.Minute, errors.New("controller installation timed out")) // TODO
		defer cancel()

		slog.Debug("Waiting for VM ssh", "name", vm.Name, "type", vm.Type)

		ticker := time.NewTicker(5 * time.Second) // TODO
		defer ticker.Stop()

	loop: // oops, some goto :)
		for {
			select {
			case <-ticker.C:
				err := vm.ssh(ctx, svcCfg, true, "hostname")
				if err != nil {
					// just waiting
					slog.Debug("Can't ssh to VM", "name", vm.Name, "type", vm.Type, "error", err)
				} else {
					break loop
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		slog.Info("VM ssh is available", "name", vm.Name, "type", vm.Type)

		// TODO k3s really don't like when we don't have default route
		// err := vm.ssh(ctx, "sudo ip route add default via 10.100.0.2 dev eth0")
		// if err != nil {
		// 	return errors.Wrap(err, "error setting default route")
		// }

		installerPath := svcCfg.ControlInstaller
		if vm.Type == VMTypeServer {
			installerPath = svcCfg.ServerInstaller
		}
		installer := filepath.Base(installerPath)

		slog.Info("Uploading installer", "name", vm.Name, "type", vm.Type, "installer", installer)
		err := vm.upload(ctx, svcCfg, false, installerPath+".tgz", "~/")
		if err != nil {
			return errors.Wrap(err, "error uploading installer")
		}
		slog.Debug("Installer uploaded", "name", vm.Name, "type", vm.Type, "installer", installer)

		slog.Info("Running installer on VM", "name", vm.Name, "type", vm.Type, "installer", installer)
		installCmd := fmt.Sprintf("tar xzf %s.tgz && cd %s && sudo ./hhfab-recipe run", installer, installer)
		if slog.Default().Enabled(ctx, slog.LevelDebug) {
			installCmd += " -v"
		}
		err = vm.ssh(ctx, svcCfg, false, installCmd)
		if err != nil {
			return errors.Wrap(err, "error installing vm")
		}

		slog.Info("VM installed", "name", vm.Name, "type", vm.Type, "installer", installer)

		err = vm.Installed.Mark()
		if err != nil {
			return errors.Wrapf(err, "error marking vm as installed")
		}

		if vm.Type != VMTypeControl {
			return nil
		}

		err = vm.download(ctx, svcCfg, true, "/etc/rancher/k3s/k3s.yaml", filepath.Join(svcCfg.Basedir, "kubeconfig.yaml"))
		if err != nil {
			return errors.Wrapf(err, "error downloading kubeconfig")
		}

		if svcCfg.InstallComplete {
			// TODO do graceful shutdown
			slog.Info("Exiting after control node installation as requested")
			os.Exit(0)
		}

		if svcCfg.RunComplete != "" {
			slog.Info("Running script after control node installation as requested")

			err = execCmd(ctx, svcCfg, "", false, svcCfg.RunComplete, []string{
				"KUBECONFIG=" + filepath.Join(svcCfg.Basedir, "kubeconfig.yaml"),
			})
			if err != nil {
				slog.Error("error running script after control node installation", "error", err)
				os.Exit(1)
			}

			// TODO do graceful shutdown
			slog.Info("Exiting after script succeded (after control node installation) as requested")
			os.Exit(0)
		}

		if len(svcCfg.OnReady) > 0 {
			slog.Info("Waiting for all switches to get ready as requested and run commands after that")
			if err := waitForSwitchesReady(svcCfg); err != nil {
				slog.Error("error waiting switches are ready", "error", err)
				os.Exit(1)
			}
		}

		for _, cmd := range svcCfg.OnReady {
			if cmd == "setup-vpcs" {
				slog.Info("Running setup-vpcs after switches are ready as requested")

				if err := svc.SetupVPCs(ctx, SetupVPCsConfig{
					Type: VPCSetupTypeVPCPerServer,
				}); err != nil {
					slog.Error("error running setup-vpcs after switches are ready", "error", err)
					os.Exit(1)
				}
			} else if strings.HasPrefix(cmd, "setup-peerings:") {
				slog.Info("Running setup-peerings after switches are ready as requested")

				// TODO
				slog.Warn("setup-peerings command is not implemented yet, skipping")
			} else if strings.HasPrefix(cmd, "test-connectivity:") {
				slog.Info("Running test-connectivity after switches are ready as requested")

				// TODO
				slog.Warn("test-connectivity command is not implemented yet, skipping")
			} else if cmd == "exit" {
				slog.Info("Exiting after switches are ready as requested")

				// TODO do graceful shutdown
				os.Exit(0)
			} else if cmd != "noop" {
				slog.Info("Running script after switches are ready as requested")

				err = execCmd(ctx, svcCfg, "", false, cmd, []string{
					"KUBECONFIG=" + filepath.Join(svcCfg.Basedir, "kubeconfig.yaml"),
				})
				if err != nil {
					slog.Error("error running script after switches are ready", "error", err)
					os.Exit(1)
				}
			}
		}

		return nil
	}

	return func() error {
		err := run(ctx)
		if err != nil {
			slog.Error("Error installing VM", "name", vm.Name, "type", vm.Type, "error", err)
		}

		return nil
	}
}

func (vm *VM) Prepare(ctx context.Context, svcCfg *ServiceConfig) error {
	if svcCfg.DryRun {
		slog.Debug("Skipping VM preparation in dry-run mode", "name", vm.Name)
		return nil
	}
	if vm.Ready.Is() {
		slog.Debug("VM is already prepared", "name", vm.Name)
		return nil
	}

	slog.Info("Preparing VM", "id", vm.ID, "name", vm.Name, "type", vm.Type)

	err := os.MkdirAll(vm.Basedir, 0o755)
	if err != nil {
		return errors.Wrapf(err, "error creating vm basedir")
	}

	files := map[string]string{}
	if vm.Type == VMTypeControl || vm.Type == VMTypeServer {
		files["os.img"] = filepath.Join(svcCfg.FilesDir, "flatcar.img")
		files["efi_code.fd"] = filepath.Join(svcCfg.FilesDir, "flatcar_efi_code.fd")
		files["efi_vars.fd"] = filepath.Join(svcCfg.FilesDir, "flatcar_efi_vars.fd")

		if vm.Type == VMTypeControl {
			files["ignition.json"] = svcCfg.ControlIgnition
		} else {
			files["ignition.json"] = filepath.Join(svcCfg.ServerIgnitionDir, fmt.Sprintf("%s.ignition.json", vm.Name))
		}
	}
	if vm.Type == VMTypeSwitchVS {
		files["os.img"] = filepath.Join(svcCfg.FilesDir, "onie-kvm_x86_64.qcow2")
		files["efi_code.fd"] = filepath.Join(svcCfg.FilesDir, "onie_efi_code.fd")
		files["efi_vars.fd"] = filepath.Join(svcCfg.FilesDir, "onie_efi_vars.fd")
	}

	err = vm.copyFiles(ctx, svcCfg, files)
	if err != nil {
		return errors.Wrapf(err, "error copying files")
	}

	slog.Info("Resizing VM image (may require sudo password)", "name", vm.Name)

	err = execCmd(ctx, svcCfg, vm.Basedir, true, "qemu-img", []string{}, "resize", "os.img", fmt.Sprintf("%dG", vm.Config.Disk))
	if err != nil {
		return errors.Wrapf(err, "error resizing image")
	}

	if vm.Type == VMTypeSwitchVS {
		onieEepromConfig, err := vm.OnieEepromConfig()
		if err != nil {
			return errors.Wrapf(err, "error generating onie-eeprom.yaml for %s", vm.Name)
		}
		err = os.WriteFile(filepath.Join(vm.Basedir, "onie-eeprom.yaml"), []byte(onieEepromConfig), 0o644)
		if err != nil {
			return errors.Wrapf(err, "error writing onie-eeprom.yaml")
		}

		slog.Info("Writing ONIE EEPROM (may require sudo password)", "name", vm.Name, "nbd", svcCfg.CharNBDDev)

		err = execCmd(ctx, svcCfg, "", true, "sudo", []string{}, filepath.Join(svcCfg.FilesDir, "onie-qcow2-eeprom-edit"),
			"--log-level=debug", "write", "--force",
			"--char-nbd-dev", svcCfg.CharNBDDev,
			"--input", filepath.Join(vm.Basedir, "onie-eeprom.yaml"),
			filepath.Join(vm.Basedir, "os.img"))
		if err != nil {
			return errors.Wrapf(err, "error writing onie-eeprom.yaml")
		}
	}

	if vm.Type == VMTypeControl || vm.Type == VMTypeSwitchVS {
		err = os.MkdirAll(filepath.Join(vm.Basedir, "tpm"), 0o755)
		if err != nil {
			return errors.Wrapf(err, "error creating tpm dir")
		}

		slog.Info("Initializing TPM (may require sudo password)", "name", vm.Name)

		err = execCmd(ctx, svcCfg, vm.Basedir, true, svcCfg.tpmSudo(), []string{},
			"swtpm_setup", "--tpm2", "--tpmstate", "tpm", "--createek", "--decryption", "--create-ek-cert", "--create-platform-cert",
			"--create-spk", "--vmid", vm.Name, "--overwrite", "--display")
		if err != nil {
			return errors.Wrapf(err, "error initializing tpm")
		}
	}

	err = vm.Ready.Mark()
	if err != nil {
		return errors.Wrapf(err, "error marking vm as ready")
	}

	slog.Debug("VM prepared", "name", vm.Name)

	return nil
}

func (vm *VM) copyFiles(ctx context.Context, cfg *ServiceConfig, names map[string]string) error {
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

func (vm *VM) ssh(ctx context.Context, svcCfg *ServiceConfig, quiet bool, command string) error {
	args := append(SSH_QUIET_FLAGS,
		"-p", fmt.Sprintf("%d", vm.sshPort()),
		"-i", svcCfg.SshKey,
		"core@127.0.0.1",
		command,
	)

	return execCmd(ctx, svcCfg, "", quiet, "ssh", []string{}, args...)
}

func (vm *VM) upload(ctx context.Context, svcCfg *ServiceConfig, quiet bool, from, to string) error {
	args := append(SSH_QUIET_FLAGS,
		"-P", fmt.Sprintf("%d", vm.sshPort()),
		"-i", svcCfg.SshKey,
		"-r",
		from,
		"core@127.0.0.1:"+to,
	)

	return execCmd(ctx, svcCfg, "", quiet, "scp", []string{}, args...)
}

func (vm *VM) download(ctx context.Context, svcCfg *ServiceConfig, quiet bool, from, to string) error {
	args := append(SSH_QUIET_FLAGS,
		"-P", fmt.Sprintf("%d", vm.sshPort()),
		"-i", svcCfg.SshKey,
		"-r",
		"core@127.0.0.1:"+from,
		to,
	)

	return execCmd(ctx, svcCfg, "", quiet, "scp", []string{}, args...)
}

func execCmd(ctx context.Context, svcCfg *ServiceConfig, basedir string, quiet bool, name string, env []string, args ...string) error {
	argsStr := strings.Join(args, " ")
	argsStr = strings.ReplaceAll(argsStr, strings.Join(SSH_QUIET_FLAGS, " "), "")

	if svcCfg.DryRun {
		slog.Debug("Dry-run, skipping command", "name", name, "args", argsStr)
		return nil
	}

	slog.Debug("Running command", "name", name, "args", argsStr)

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = basedir
	cmd.Env = append(os.Environ(), env...)

	logFileName := filepath.Join(basedir, fmt.Sprintf("exec-%d.log", time.Now().UnixMilli()))
	logFile, err := os.OpenFile(logFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return errors.Wrapf(err, "error opening log file %s", logFileName)
	}
	defer logFile.Close()

	outputs := []io.Writer{logFile}

	if !quiet || slog.Default().Enabled(ctx, slog.LevelDebug) {
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
