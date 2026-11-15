package put

type Putter struct {
	deps Dependencies
}

func New(deps Dependencies) (*Putter, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Putter{deps: deps}, nil
}

var _ Service = (*Putter)(nil)
