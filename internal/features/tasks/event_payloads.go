package tasks

type TasksUpdatedPayload struct {
	Tasks []Task `json:"tasks"`
}
type TaskContinuedPayload struct {
	TaskID                string `json:"task_id"`
	Status                Status `json:"status"`
	Reason                string `json:"reason,omitempty"`
	ContinuationCount     int    `json:"continuation_count"`
	ContinuationPromptLen int    `json:"continuation_prompt_len"`
}
