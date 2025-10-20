package pipeline

type noopStage struct {
	name    string
	message string
}

// NewNoopStage returns a placeholder stage that always succeeds.
func NewNoopStage(name, message string) Stage {
	return &noopStage{name: name, message: message}
}

func (s *noopStage) Name() string {
	return s.name
}

func (s *noopStage) Run(ctx *Context) Result {
	return Result{Status: StatusSuccess, Message: s.message}
}
