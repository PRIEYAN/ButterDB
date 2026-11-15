package put

type Putter struct {
	deps   Dependencies
	locker *KeyedLocker
}

func New(deps Dependencies) (*Putter, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Putter{deps: deps, locker: NewKeyedLocker()}, nil
}

var _ Service = (*Putter)(nil)
