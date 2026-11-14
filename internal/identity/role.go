package identity

type Role string

const (
	RolePrimary Role = "primary"
	RoleReplica Role = "replica"
)

func (r Role) valid() bool {
	return r == RolePrimary || r == RoleReplica
}
