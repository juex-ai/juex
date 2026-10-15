package agent

func (a *Agent) checkInput(id string, request TurnAdmissionRequest) error {
	if a.executionPolicy.CheckInput == nil {
		return nil
	}
	return a.executionPolicy.CheckInput(id, request)
}

func (a *Agent) parseCommand(input string) (Command, bool, error) {
	if a.executionPolicy.ParseCommand == nil {
		return Command{}, false, nil
	}
	return a.executionPolicy.ParseCommand(input)
}

func (a *Agent) attachmentWarnings(count int) []TurnWarning {
	if a.executionPolicy.AttachmentWarnings == nil {
		return nil
	}
	return a.executionPolicy.AttachmentWarnings(count)
}
