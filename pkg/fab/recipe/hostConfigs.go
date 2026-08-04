package recipe

// Policy - Only add a file / setting to butane file if we need it before running the install recipe
// The rest should be orderd in the install or upgrade recipe as needed.
// The initial build and upgrade will need to pick values from the config for templating

// sshd drop in
// nftables
// network interace files etc
type systemdFile struct {
	tmpl       string
	tmplValues map[string]string
	startCmd   string
	stopCmd    string
	verifyCmd  string // systemd-analyze verify
	dstPath    string
	pritority  string // 10,20,30-filename.network
	fileName   string
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
//
/* Code from an online example, tbd if it applies
	package main

	import "fmt"

	// Define an interface
	type Container interface {
    Add(value interface{})
    Get(index int) interface{}
	}

	// Implement a simple container
	type MyContainer struct {
    items []interface{}
	}

	func (c *MyContainer) Add(value interface{}) {
    c.items = append(c.items, value)
	}

	func (c *MyContainer) Get(index int) interface{} {
    if index < 0 || index >= len(c.items) {
        return nil
    }
    return c.items[index]
	}

	func main() {
    container := &MyContainer{}
    container.Add(42)          // Adding an int
    container.Add("Hello")     // Adding a string
    container.Add(3.14)       // Adding a float

    fmt.Println(container.Get(0)) // Outputs: 42
    fmt.Println(container.Get(1)) // Outputs: Hello
    fmt.Println(container.Get(2)) // Outputs: 3.14
	}
*/
