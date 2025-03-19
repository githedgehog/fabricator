package v1beta1

const (
	ns = "fabricator.githedgehog.com"

	RoleLabelValue = "true"
	RoleTaintValue = RoleLabelValue
)

func RoleLabelKey(role NodeRole) string {
	return "role." + ns + "/" + string(role)
}

func RoleTaintKey(role NodeRole) string {
	return RoleLabelKey(role)
}
