package events

func Required(eventType string, factory func() any, browserVisible bool) Definition {
	return Definition{
		Type: eventType, Version: 1, ReplayPolicy: ReplayRequired,
		BrowserVisible: browserVisible, NewPayload: factory,
	}
}

func Ignorable(eventType string, factory func() any, browserVisible bool) Definition {
	return Definition{
		Type: eventType, Version: 1, ReplayPolicy: ReplayIgnorable,
		BrowserVisible: browserVisible, NewPayload: factory,
	}
}

func IgnorableValidated(eventType string, factory func() any, browserVisible bool, validate func(any) error) Definition {
	return Definition{
		Type: eventType, Version: 1, ReplayPolicy: ReplayIgnorable,
		BrowserVisible: browserVisible, NewPayload: factory, Validate: validate,
	}
}

func Transient(eventType string, factory func() any) Definition {
	return TransientVersioned(eventType, 1, factory)
}

func TransientVersioned(eventType string, version int, factory func() any) Definition {
	return Definition{
		Type: eventType, Version: version,
		Transient: true, BrowserVisible: true, NewPayload: factory,
	}
}

func RequiredValidated(eventType string, version int, factory func() any, browserVisible bool, validate func(any) error) Definition {
	return Definition{
		Type: eventType, Version: version, ReplayPolicy: ReplayRequired,
		BrowserVisible: browserVisible, NewPayload: factory, Validate: validate,
	}
}
