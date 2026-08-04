package recipe

// Policy - Only add a file / setting to butane file if we need it before running the install recipe
// The rest should be orderd in the install or upgrade recipe as needed.

// sshd drop in
// nftables
type unitFile struct {
	tmpl       string
	tmplValues map[string]string
	startCmd   string
	stopCmd    string
	verifyCmd  string // systemd-analyze verify
	dstPath    string
	pritority  string // 10,20,30-filename.network
}

type networkFile struct {
	tmpl       string
	tmplValues map[string]string
	startCmd   string
	stopCmd    string
	verifyCmd  string // systemd-analyze verify
	dstPath    string
	pritority  string // 10,20,30-filename.network
}

// hostnamectl
// timedatectl
// iptables vxlan command
type cmdSetting struct {
	binPath   string
	args      string
	startCmd  string
	stopCmd   string
	verifyCmd string
	dstPath   string
}

// sysctl file
// modules.d file
type settingsFile struct {
	fileContents string
	dstPath      string
}

type ConfigAction interface {
	enforce() (bool, error)
	render() (string, error)
	activate() error
	deactivate() error
}
