package coordinator

// LockMode identifies a shared reader or exclusive writer lease on a resource.
type LockMode string

const (
	RLock LockMode = "R"
	WLock LockMode = "W"
)
