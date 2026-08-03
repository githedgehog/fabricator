package recipe

type hostConfig struct {
	tmpl       string
	tmplValues map[string]string
	startCmd   string
	stopCmd    string
	verifyCmd  string
	dstPath    string
}

type ConfigAction interface {
	enforce() error
	render() (string, error)
	activate() error
	deactivate() error
}
