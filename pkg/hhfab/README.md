**How to Build OVMF (CODE and VARS) using EDK2**

The Open Virtual Machine Firmware (OVMF) project aims to support firmware for Virtual Machines using the edk2 code base.  

1 - Getting Started with EDK II

Follow the steps described at https://github.com/tianocore/tianocore.github.io/wiki/Using-EDK-II-with-Native-GCC:

Some dependencies:
```$ sudo apt install build-essential uuid-dev iasl git  nasm  python-is-python3```

Cloning edk2:
```$ git clone https://github.com/tianocore/edk2```

Initialize submodules:
```
$ cd edk2
$ git submodule update --init
```
Compile build tools:
```
$ make -C BaseTools
$ ./edksetup.sh
```
Setup build shell environment:
```
$ export EDK_TOOLS_PATH=$HOME/src/edk2/BaseTools
$ ./edksetup.sh BaseTools
```

Modify Conf Files - edk2/Conf/target.txt:
```
ACTIVE_PLATFORM = EmulatorPkg/EmulatorPkg.dsc
TOOL_CHAIN_TAG  = GCC114
TARGET_ARCH     = X64
```
Modify EmulatorPkg Files - edk2/EmulatorPkg/EmulatorPkg.dsc:
```
#
# Network definition
#
DEFINE NETWORK_SNP_ENABLE       = FALSE
DEFINE NETWORK_IP6_ENABLE       = FALSE
DEFINE NETWORK_TLS_ENABLE       = FALSE
DEFINE NETWORK_HTTP_BOOT_ENABLE = FALSE
DEFINE NETWORK_HTTP_ENABLE      = FALSE
DEFINE NETWORK_ISCSI_ENABLE     = FALSE
DEFINE SECURE_BOOT_ENABLE       = FALSE
```
Note: 
```
$gcc --version
gcc (Ubuntu 11.4.0-1ubuntu1~22.04) 11.4.0
```
Once you have modified Conf/target.txt and EmulatorPkg/EmulatorPkg.dsc, you can run the build command:
```
$ cd edk2/OvmfPkg
$ ./build.sh
```
If successful, you should now have an OVMF.Fd file under the Build sub-directory. The exact directory under the Build directory will depend upon the toolchain, dsc, and processor architecture:
```
$cd edk2/Build/OvmfX64/DEBUG_GCC5/FV/
$ ls
OVMF.fd
OVMF_VARS.fd
```
Files used in hhfab/vlab:
```
OVMF.fd = onie_efi_code.fd
OVMF_VARS.fd = onie_efi_vars.fd
onie-kvm_x86_64.qcow2 - To generate this file, follow the procedure described at https://github.com/githedgehog/onie_kvm.
```
References:
https://github.com/tianocore/tianocore.github.io/wiki/Getting-Started-with-EDK-II
https://github.com/tianocore/tianocore.github.io/wiki/How-to-build-OVMF
https://github.com/tianocore/edk2/blob/master/OvmfPkg/README
http://www.tianocore.org/ovmf/
https://github.com/tianocore/
https://github.com/tianocore/tianocore.github.io/wiki/Training
https://github.com/tianocore/tianocore.github.io/wiki/UEFI-EDKII-Learning-Dev
