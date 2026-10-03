package change

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Classify assigns a risk tier to one op, with a plain-words reason.
// The rule of thumb: anything that can destroy data that no inverse can
// bring back is irreversible; anything that exposes data to the outside
// world is outbound; the rest can be undone by applying the inverse.
func Classify(op Op) (Tier, string) {
	kind, name := Kind(op.Address), Name(op.Address)
	switch op.Action {
	case Create:
		return TierReversible, "new " + kindNoun(kind, name) + "; undo deletes it"
	case Delete:
		switch kind {
		case KindService:
			switch name {
			case "auth":
				return TierIrreversible, "deletes every user account, session and organization of the project"
			case "analytics":
				return TierIrreversible, "deletes all collected analytics events"
			case "email":
				return TierReversible, "removes the email service; undo restores it"
			}
			return TierIrreversible, fmt.Sprintf("deletes the %s service and all its data", name)
		case KindCron:
			return TierReversible, fmt.Sprintf("removes cron %q; undo restores it", name)
		case KindBucket:
			return TierIrreversible, fmt.Sprintf("deletes bucket %q and every file in it", name)
		case KindProject:
			return TierIrreversible, "deletes the project"
		case KindApp:
			return TierReversible, fmt.Sprintf("stops and removes app %q; its builds are kept so undo restores it", name)
		default:
			return TierReversible, "removes " + kindNoun(kind, name) + "; undo restores it"
		}
	case Update:
		switch kind {
		case KindService:
			if name == "postgres" {
				if dropped := droppedExtensions(op.Before, op.After); len(dropped) > 0 {
					return TierIrreversible, "drops Postgres extension(s) " + strings.Join(dropped, ", ") + " and every column, index or job that uses them"
				}
			}
			if name == "analytics" && shortenedRetention(op.Before, op.After) {
				return TierIrreversible, "shortens analytics retention and deletes events older than the new limit"
			}
		case KindBucket:
			if becamePublic(op.Before, op.After) {
				return TierOutbound, fmt.Sprintf("makes bucket %q publicly readable by anyone with a link", name)
			}
		}
		return TierReversible, "updates " + kindNoun(kind, name) + "; undo restores the previous settings"
	}
	return Tier("unknown"), "unknown action"
}

func kindNoun(kind, name string) string {
	switch kind {
	case KindProject:
		return "project"
	case KindEnv:
		return "env var " + name
	case KindService:
		return name + " service"
	}
	return kind + " " + name
}

func droppedExtensions(before, after json.RawMessage) []string {
	var a, b struct {
		Extensions []string `json:"extensions"`
	}
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	var dropped []string
	for _, e := range a.Extensions {
		if !slices.Contains(b.Extensions, e) {
			dropped = append(dropped, e)
		}
	}
	return dropped
}

func becamePublic(before, after json.RawMessage) bool {
	var a, b struct {
		Public bool `json:"public"`
	}
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	return !a.Public && b.Public
}

func shortenedRetention(before, after json.RawMessage) bool {
	var a, b struct {
		RetentionDays int `json:"retentionDays"`
	}
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	return b.RetentionDays < a.RetentionDays
}
