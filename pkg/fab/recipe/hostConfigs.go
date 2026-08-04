package recipe

// Policy - Only add a file / setting to butane file if we need it before running the install recipe
// The rest should be orderd in the install or upgrade recipe as needed.
// The initial build and upgrade will need to pick values from the config for templating

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

// dummy interface
// mgmt interface
// ext interface
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

// this interface is what lets me do all the changes on diverse sets of interfaces
type Configuration interface {
	enforce() (bool, error)
	render() (string, error)
	activate() error
	deactivate() error
}

// TODO make the settingsFile, cmdSettings, networkFile, unitFil  into a single slice or similar so I can iterate through the slice and call enforce on them
// TODO
// TODO things inside the struct will need to be templated, ho
