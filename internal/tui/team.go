package tui

import (
	"fmt"
	"sort"
	"strings"
)

// The `t` panel: the team board. Where the `m` panel answers "what is
// the mesh state right now" and `s` answers "what can be called", the
// team board answers "who is working on what" -- the coordination the
// mesh already carries in its message kinds, extracted and presented as
// one view: open lanes, open handoffs, recent results, help broadcasts,
// and the roster. Everything here is derived from the same meshState
// snapshot the other panels use; no extra fetching, and the derivation
// helpers are pure functions so the board's logic is testable without a
// running terminal.

// renderTeamOverlay is the `t` panel's overlay, same shape as `m`/`s`/`r`
// and mutually exclusive with them.
func (m Model) renderTeamOverlay() string {
	return m.renderOverlay(panelStyle.Render(m.renderTeam()))
}

func (m Model) renderTeam() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Team") + "\n")

	sections := []struct {
		title string
		body  string
	}{
		{"Open lanes", m.renderTeamItems(openLanes(m.state.recent), "lane")},
		{"Open handoffs", m.renderTeamItems(openHandoffs(m.state.recent), "handoff")},
		{"Recent results", m.renderTeamItems(recentResults(m.state.recent, 5), "result")},
		{"Help", m.renderTeamItems(helpBroadcasts(m.state.central, 5), "help")},
		{"Roster", m.renderTeamRoster()},
		{"Team rooms", m.renderTeamRooms()},
	}

	any := false
	for _, s := range sections {
		if s.body == "" {
			continue
		}
		any = true
		b.WriteString(s.title + "\n")
		b.WriteString(s.body + "\n\n")
	}
	if !any {
		b.WriteString(m.panelPlaceholder("no team activity on the mesh right now"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTeamItems renders one row per team item: the actor's name in its
// deterministic identity color, the text, and the room it happened in.
func (m Model) renderTeamItems(items []teamItem, noun string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, it := range items {
		who := agentBadgeStyle(identityKey(it.who, it.petname)).Render(displayName(it.who, it.petname))
		line := "    " + who + dimStyle.Render(": "+truncate(it.text, 90))
		if it.room != "" {
			line += dimStyle.Render("  (" + it.room + ")")
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTeamRoster shows who is on the field: every agent seen, with its
// model and how recently it spoke. Same identity-color principle as the
// presence panel; deliberately narrower (no session/via columns) --
// those are self-reported plumbing, and this board is about work.
func (m Model) renderTeamRoster() string {
	if len(m.state.agents) == 0 {
		return ""
	}
	agents := make([]agentPresence, len(m.state.agents))
	copy(agents, m.state.agents)
	sort.SliceStable(agents, func(i, j int) bool {
		return agents[i].SecondsSinceSeen < agents[j].SecondsSinceSeen
	})
	var b strings.Builder
	for _, a := range agents {
		who := agentBadgeStyle(identityKey(a.NodeID, a.Petname)).Render(displayName(a.NodeID, a.Petname))
		line := "    " + who
		if a.Model != "" {
			line += dimStyle.Render(" · " + a.Model)
		}
		line += dimStyle.Render(" · seen " + ago(a.SecondsSinceSeen))
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTeamRooms lists the public rooms announced on central: the team
// spaces other agents have opened, with who opened each.
func (m Model) renderTeamRooms() string {
	if len(m.state.publicRooms) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range m.state.publicRooms {
		who := agentBadgeStyle(identityKey(r.OpenedBy, "")).Render(displayName(r.OpenedBy, ""))
		label := roomLabel(r.RoomTopic, r.Purpose)
		b.WriteString("    " + dimStyle.Render(label+"  (by "+who+")") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// teamItem is one row of team activity: who did something, what it was,
// in which room, when.
type teamItem struct {
	who     string // node id
	petname string
	text    string
	room    string // short room topic; empty for lobby broadcasts
	sentAt  int64  // epoch millis
}

// openLanes finds lane_claimed messages with no lane_released answering
// them: work lanes currently held (or abandoned without release).
func openLanes(recent map[string][]roomMessage) []teamItem {
	return openPairs(recent, "lane_claimed", "lane_released")
}

// openHandoffs finds task_handed_over messages with no result_reported
// answering them: work delegated but not yet reported back.
func openHandoffs(recent map[string][]roomMessage) []teamItem {
	return openPairs(recent, "task_handed_over", "result_reported")
}

// openPairs is the shared derivation: every `claim`-kind message whose
// message_id is not referenced as the in_reply_to of a `release`-kind
// message counts as still open.
func openPairs(recent map[string][]roomMessage, claim, release string) []teamItem {
	released := make(map[string]bool)
	for _, msgs := range recent {
		for _, m := range msgs {
			if m.Kind == release && m.InReplyTo != "" {
				released[m.InReplyTo] = true
			}
		}
	}
	var items []teamItem
	for topic, msgs := range recent {
		for _, m := range msgs {
			if m.Kind == claim && !released[m.MessageID] {
				items = append(items, teamItem{
					who:     m.From,
					petname: m.FromPetname,
					text:    m.Text,
					room:    shortTopic(topic),
					sentAt:  m.SentAt,
				})
			}
		}
	}
	sortTeamItems(items)
	return items
}

// recentResults collects the newest result_reported messages across all
// rooms, newest first, capped at n.
func recentResults(recent map[string][]roomMessage, n int) []teamItem {
	var items []teamItem
	for topic, msgs := range recent {
		for _, m := range msgs {
			if m.Kind == "result_reported" {
				items = append(items, teamItem{
					who:     m.From,
					petname: m.FromPetname,
					text:    m.Text,
					room:    shortTopic(topic),
					sentAt:  m.SentAt,
				})
			}
		}
	}
	sortTeamItems(items)
	if len(items) > n {
		items = items[len(items)-n:]
	}
	return items
}

// helpBroadcasts picks the newest help_requested/help_offered broadcasts
// off the lobby, newest first, capped at n.
func helpBroadcasts(central []roomMessage, n int) []teamItem {
	var items []teamItem
	for _, m := range central {
		if m.Kind == "help_requested" || m.Kind == "help_offered" {
			items = append(items, teamItem{
				who:     m.From,
				petname: m.FromPetname,
				text:    m.Text,
			})
		}
	}
	sortTeamItems(items)
	if len(items) > n {
		items = items[len(items)-n:]
	}
	return items
}

// sortTeamItems orders team items oldest-first by sent time; help
// broadcasts and inbox messages both carry epoch-millis sent_at, and the
// room field is the only payload difference, so one comparison serves
// both. Deterministic regardless of map iteration order.
func sortTeamItems(items []teamItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].sentAt < items[j].sentAt
	})
}

// ago renders a seconds-since-seen age compactly.
func ago(seconds int) string {
	switch {
	case seconds < 0:
		return "now"
	case seconds < 60:
		return fmt.Sprintf("%ds ago", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm ago", seconds/60)
	default:
		return fmt.Sprintf("%dh ago", seconds/3600)
	}
}
