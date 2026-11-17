package queue

// Priority selects one of the two durable scheduling lanes. A priority never
// preempts an active handler or bypasses type, resource, or exclusivity limits.
type Priority int

const (
	PriorityNormal Priority = iota
	PriorityInteractive
)

func (priority Priority) Validate() error {
	if priority != PriorityNormal && priority != PriorityInteractive {
		return Fail(CodeConfigInvalid, "priority must be Normal or Interactive")
	}
	return nil
}

// WithPriority sets the enqueue lane. The default is Normal. Dedupe returns
// existing work unchanged, even when the new request has a higher priority.
func WithPriority(priority Priority) Option {
	return func(options *enqueueOptions) {
		if err := priority.Validate(); err != nil {
			options.err = err
			return
		}
		options.priority = priority
	}
}

// PromotionOption adjusts an explicit promotion of pending work.
type PromotionOption func(*promotionOptions)

type promotionOptions struct{ readyNow bool }

// Promotion is the validated request shared by the runtime and operator.
// ResolvePromotion is its constructor; its fields cannot be changed by callers.
type Promotion struct {
	priority Priority
	readyNow bool
}

// ReadyNow explicitly removes any remaining delay when promoting pending
// work. Without it, promotion preserves the original availability time.
func ReadyNow() PromotionOption {
	return func(options *promotionOptions) { options.readyNow = true }
}

// ResolvePromotion validates the closed promotion options.
func ResolvePromotion(priority Priority, options ...PromotionOption) (Promotion, error) {
	if err := priority.Validate(); err != nil {
		return Promotion{}, err
	}
	resolved := promotionOptions{}
	for _, option := range options {
		if option == nil {
			return Promotion{}, Fail(CodeConfigInvalid, "promotion received a nil option")
		}
		option(&resolved)
	}
	return Promotion{priority: priority, readyNow: resolved.readyNow}, nil
}

func (promotion Promotion) Priority() Priority { return promotion.priority }
func (promotion Promotion) ReadyNow() bool     { return promotion.readyNow }

// Priority reports the lane recorded when this work is enqueued.
func (pending Pending) Priority() Priority { return pending.priority }
