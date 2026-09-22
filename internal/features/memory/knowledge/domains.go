// Package knowledge owns Memory's declarative domains and fact semantics.
package knowledge

import (
	"fmt"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

const policy = "No automatic decay. Original evidence is required; silence and recall are not confirmation. Confirm by adding sources without refreshing effective time. Keep past truth distinct from correction, retraction and dispute. Tasks and Calendar own execution and reminders."

var updates = []string{"add", "confirm", "parallel", "supersede", "correct", "retract", "dispute"}

func relation(predicate, description, subject, object, value, cardinality, positive, negative string, qualifiers ...string) mc.Relation {
	competition := []string{"subject", "predicate", "workspace", "project", "context"}
	competition = append(competition, qualifiers...)
	return mc.Relation{OptionalQualifiers: []string{"context", "strength"}, SourceTypes: []string{"user_statement", "self_report", "observation", "derived"}, Predicate: predicate, Description: description, Subjects: []string{subject}, Objects: splitType(object), ValueType: value, Qualifiers: qualifiers, Cardinality: cardinality, Competition: competition,
		Temporal: "Half-open effective interval [valid_from, valid_until); unknown endpoints stay unspecified and are not guessed by as-of queries. Supersession ends past truth; correction rejects an earlier error. No expiry inference from silence.", Updates: updates,
		Evidence: "Direct original evidence with source and recorded time; effective time only when supported. New confirmations and changes require current assignment user evidence. Do not infer sensitive attributes.", Positive: positive, Negative: negative}
}
func splitType(t string) []string {
	if t == "" {
		return nil
	}
	return []string{t}
}
func domain(id, name, description string, relations ...mc.Relation) mc.Domain {
	return mc.Domain{ID: id, Name: name, Description: description, Relations: relations, Policy: policy}
}

var domains = []mc.Domain{
	domain("identity", "Identity and self", "Identity, residence, values and scoped employment.",
		relation("identifies_as", "Explicit self-description", "person", "", "text", "many", "User describes themselves as an engineer.", "Do not infer personality from tone."),
		relation("employed_by", "Employment within a stated role/engagement", "person", "organization", "", "one", "Two employers may coexist under separate engagement IDs.", "A second job does not end the first.", "engagement", "role"),
		relation("resides_in", "Current residence", "person", "place", "", "one", "An explicit completed move closes the previous residence interval.", "Considering a move does not replace residence."),
		relation("intends_residence", "Unfulfilled residence intention", "person", "place", "", "many", "Considering Hangzhou is an intention.", "Do not treat an intention as a completed move."),
		selfReported(relation("mbti", "Dated explicit MBTI self-report", "person", "", "text", "one", "User reports their own test result with its supported date.", "Never infer MBTI from conversation.")),
		derived(relation("zodiac", "Explicitly derived birthday label", "person", "", "text", "one", "A derivation retains its dated birthday evidence.", "Do not present a derived label as a direct user statement.")),
		relation("values", "Explicit personal value", "person", "", "text", "many", "User values privacy and independence.", "Do not infer values from one click.")),
	domain("interpersonal", "Interpersonal relations", "Explicit entity relationships; names alone never establish identity.",
		relation("family_of", "Family relationship, oriented from subject to other person", "person", "person", "", "many", "User identifies Alex as their sibling with role=sibling.", "Two people named Alex must not be merged.", "role"),
		relation("friend_of", "Friendship", "person", "person", "", "many", "An explicit ended friendship closes its interval.", "Lack of contact does not end a friendship."),
		relation("colleague_of", "Colleague in an organization", "person", "person", "", "many", "Colleagues at different organizations coexist.", "Do not generalize an organization-local relationship.", "organization"),
		relation("partner_of", "Explicit partnership", "person", "person", "", "many", "Preserve an ended partnership as historical.", "Do not infer a partner from co-location.")),
	domain("knowledge", "Knowledge and interests", "Interests, study and research; interest is not demonstrated mastery.",
		relation("interested_in", "Knowledge interest", "person", "topic", "", "many", "Interest in AI and biology can coexist.", "Interest does not imply expertise."),
		relation("studies", "Current study", "person", "topic", "", "many", "Explicitly studying Japanese.", "A wish to learn does not prove mastery."),
		relation("researches", "Research subject", "person", "topic", "", "many", "Explicit current research topic.", "Reading one article does not establish a research career.")),
	domain("health", "Health", "Only explicitly supported conditions, treatments and recovery.",
		relation("has_condition", "Explicit condition", "person", "condition", "", "many", "Two reported conditions coexist.", "Silence does not mean recovered."),
		relation("receives_treatment", "Explicit treatment", "person", "treatment", "", "many", "Treatment may coexist with its condition.", "Do not infer a diagnosis from medication."),
		relation("recovered_from", "Explicit recovery", "person", "condition", "", "many", "Reported recovery can close the matching condition with evidence.", "Expiry of an appointment is not recovery.")),
	domain("projects", "Projects and career", "Project-qualified participation, responsibility, technology and decisions.",
		relation("participates_in", "Project participation", "person", "project", "", "many", "Participation in several projects is valid.", "Project activity is not Tasks completion."),
		relation("responsible_for", "Project responsibility", "person", "project", "", "many", "User owns deployment for project A.", "Do not apply a role from A to B.", "role"),
		relation("uses_technology", "Technology within a project", "project", "topic", "", "many", "Project A uses Go; project B uses Python.", "Technology choices do not overwrite another project."),
		relation("decided", "Decision in a named decision area", "project", "", "text", "one", "A new explicit database decision supersedes the previous one in that area.", "Separate projects and decision areas never compete.", "area")),
	domain("hobbies", "Hobbies", "Practice, participation and equipment without inferred abandonment.",
		relation("practices", "Practiced activity", "person", "activity", "", "many", "Climbing and photography coexist.", "Dormancy is not abandonment."),
		relation("participates_in_event", "Activity event participation", "person", "event", "", "many", "Explicit club event participation.", "An invitation is not attendance."),
		relation("uses_equipment", "Equipment for an activity", "person", "item", "", "many", "A named camera for photography.", "Using equipment does not imply ownership.", "activity")),
	domain("preferences", "Preferences and habits", "Additions, strength, negation and context remain distinct.",
		relation("prefers", "Positive preference", "person", "", "text", "many", "Also likes photography preserves climbing.", "A new preference does not remove the earlier one."),
		relation("avoids", "Explicit avoidance", "person", "", "text", "many", "Avoiding coffee is separately supported.", "Absence of a preference is not avoidance."),
		relation("habit", "Reported habit", "person", "", "text", "many", "An explicit daily reading habit.", "One occurrence does not establish a habit.")),
	domain("finance", "Finance and material", "Explicit amounts, currency, periods and asset/contract scopes.",
		relation("income", "Income amount per currency/period/engagement", "person", "", "number", "one", "Two employments may have different monthly income.", "Never sum different currencies or periods implicitly.", "currency", "period", "engagement"),
		relation("rent", "Rent amount for a contract", "person", "", "number", "one", "A contract can change rent with an effective date.", "Different rental contracts do not compete.", "currency", "period", "contract"),
		relation("holds", "Quantity held in an asset", "person", "", "number", "one", "Explicit quantity of a named asset in an account.", "Do not infer holdings from interest in an asset.", "asset", "account", "unit"),
		relation("owns", "Explicit ownership", "person", "item", "", "many", "User explicitly owns a camera.", "Mentioning a camera does not imply ownership.")),
	domain("obligations", "Obligations", "Commitments and requirements; deadlines do not imply completion.",
		relation("committed_to", "Explicit commitment", "person", "obligation", "", "many", "A commitment past due_at remains overdue until evidence ends it.", "A passed deadline cannot mark it completed."),
		relation("required_to", "Explicit requirement", "person", "obligation", "", "many", "Requirement retains its source and due_at.", "Memory does not schedule reminders or update Tasks.")),
	domain("temporary", "Temporary context", "Bounded locations and one-off arrangements; no implicit scheduling.",
		relation("temporarily_at", "Temporary location", "person", "place", "", "one", "Explicit trip interval is distinct from residence.", "A trip does not replace permanent residence."),
		relation("arranged", "One-off arrangement", "person", "event", "", "many", "An arrangement uses supported effective boundaries or a time_note for uncertainty.", "Do not invent an exact date from ambiguous relative time.")),
	domain("other", "Other / open", "Supported facts outside the declared relations remain explicitly open.",
		relation("open_fact", "Open supported statement; qualifier topic describes its meaning", "any", "", "text", "many", "An undeclared preference detail may remain an explicit open statement.", "Do not invent canonical synonyms or force an unsuitable domain.", "topic"),
		relation("open_relation", "Open relation with explicit topic", "any", "any", "", "many", "An undeclared relation uses explicit identities and a topic.", "Do not create ontology synonyms at runtime.", "topic")),
}

func Domains(id string) ([]mc.Domain, error) {
	if id == "" {
		out := make([]mc.Domain, len(domains))
		for i, d := range domains {
			d.Relations = nil
			out[i] = d
		}
		return out, nil
	}
	for _, d := range domains {
		if d.ID == id {
			return []mc.Domain{d}, nil
		}
	}
	return nil, fmt.Errorf("memory unknown domain %q", id)
}
func Relation(domain, predicate string) (mc.Relation, bool) {
	for _, d := range domains {
		if d.ID == domain {
			for _, r := range d.Relations {
				if r.Predicate == predicate {
					return r, true
				}
			}
		}
	}
	return mc.Relation{}, false
}

func selfReported(r mc.Relation) mc.Relation { r.SourceTypes = []string{"self_report"}; return r }
func derived(r mc.Relation) mc.Relation      { r.SourceTypes = []string{"derived"}; return r }
