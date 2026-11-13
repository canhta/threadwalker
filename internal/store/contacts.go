package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// NormalizeUsername is the shared contact key. Usernames are identifiers only;
// arbitrary descriptions, URLs and whitespace are never valid contact data.
func NormalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	if len(username) == 0 || len(username) > 30 {
		return "", errors.New("normalizing username: invalid identifier")
	}
	for _, r := range username {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
			return "", errors.New("normalizing username: invalid identifier")
		}
	}
	return username, nil
}

// MarkDoNotContact is permanent and add-only, even for a paused agent. Reasons
// are fixed source codes, never message content or descriptions of a person.
func (s *Store) MarkDoNotContact(ctx context.Context, agentID, username, reason string) error {
	username, err := NormalizeUsername(username)
	if err != nil {
		return err
	}
	switch reason {
	case "owner_request", "agent_request", "stop_request":
	default:
		return errors.New("marking do not contact: invalid reason code")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO contacts(agent_id,username,do_not_contact,reason) SELECT id,?,1,? FROM agents WHERE id=? ON CONFLICT(agent_id,username) DO UPDATE SET do_not_contact=1,reason=CASE WHEN contacts.do_not_contact=1 THEN contacts.reason ELSE excluded.reason END`, username, reason, agentID)
	if err != nil {
		return fmt.Errorf("marking do not contact: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking contact update: %w", err)
	}
	if n != 1 {
		return errors.New("marking do not contact: agent missing")
	}
	return nil
}
