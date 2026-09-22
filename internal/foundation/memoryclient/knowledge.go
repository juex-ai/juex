package memoryclient

// Domain and Relation are wire descriptions. Memory Feature owns their definitions.
type DomainRequest struct {
	ID string `json:"id,omitempty"`
}
type Domain struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Relations   []Relation `json:"relations,omitempty"`
	Policy      string     `json:"policy"`
}
type Relation struct {
	OptionalQualifiers []string `json:"optional_qualifiers,omitempty"`
	SourceTypes        []string `json:"source_types"`
	Predicate          string   `json:"predicate"`
	Description        string   `json:"description"`
	Subjects           []string `json:"subjects"`
	Objects            []string `json:"objects,omitempty"`
	ValueType          string   `json:"value_type,omitempty"`
	Qualifiers         []string `json:"qualifiers,omitempty"`
	Cardinality        string   `json:"cardinality"`
	Competition        []string `json:"competition"`
	Temporal           string   `json:"temporal"`
	Updates            []string `json:"updates"`
	Evidence           string   `json:"evidence"`
	Positive           string   `json:"positive"`
	Negative           string   `json:"negative"`
}
type FactView struct {
	EntryID   string  `json:"entry_id"`
	Revision  uint64  `json:"revision"`
	Scope     Scope   `json:"scope"`
	Fact      Fact    `json:"fact"`
	Subject   Entity  `json:"subject"`
	Object    *Entity `json:"object,omitempty"`
	Lifecycle string  `json:"lifecycle"`
}
type FactPage struct {
	Facts       []FactView `json:"facts"`
	Next        int        `json:"next"`
	Total       int        `json:"total"`
	DomainTotal int        `json:"domain_total"`
	Fence       uint64     `json:"fence"`
}
