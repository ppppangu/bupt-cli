package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// PortalContent is the typed consumer view of the existing portal.read envelope.
// The controller's public JSON and older on-disk Data maps share this wire shape.
type PortalContent[T any] struct {
	PortalObservation
	Account *PortalAccount `json:"account,omitempty"`
	Data    T              `json:"data"`
}

type PortalObservation struct {
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Warnings   []string  `json:"warnings,omitempty"`
}

func (p *PortalContent[T]) UnmarshalJSON(raw []byte) error {
	var envelope struct {
		PortalObservation
		Account *PortalAccount `json:"account"`
		Data    *T             `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return E("PORTAL_PROTOCOL", "门户响应缺少该栏目所需的数据，按 r 可重新读取。")
	}
	p.PortalObservation, p.Account, p.Data = envelope.PortalObservation, envelope.Account, *envelope.Data
	return nil
}

// ValidateContent rejects malformed successful school responses before they
// can replace the last usable offline snapshot. The public data stays intact.
func (p PortalPage) ValidateContent() error {
	var target any
	switch p.Section {
	case "agreement", "news.show":
		target = new(PortalDocument)
	case "news":
		target = new(PortalNews)
	case "leaderboard":
		target = new(PortalLeaderboard)
	case "preference":
		target = new(LeaderboardPreference)
	case "usage":
		target = new(PortalUsage)
	default:
		return nil
	}
	b, err := json.Marshal(p.Data)
	if err != nil || p.Data == nil || json.Unmarshal(b, target) != nil {
		return E("PORTAL_PROTOCOL", "门户返回的数据与该栏目格式不符。")
	}
	return nil
}

type PortalDocument struct {
	ID          string
	Title       string
	Body        string
	Version     string
	PublishedAt string
}

type documentWire struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	BodyMd      string `json:"bodyMd"`
	Version     string `json:"version"`
	PublishedAt string `json:"publishedAt"`
}

// Both school endpoints return the document at top level. Old snapshots may
// wrap it in "agreement" or "item".
func (d *PortalDocument) UnmarshalJSON(raw []byte) error {
	var obj struct {
		documentWire
		Agreement, Item *documentWire
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	wire := obj.documentWire
	if obj.Agreement != nil {
		wire = *obj.Agreement
	} else if obj.Item != nil {
		wire = *obj.Item
	}
	body := wire.BodyMd
	if body == "" {
		body = wire.Body
	}
	if wire.ID == "" || wire.Title == "" || strings.TrimSpace(body) == "" {
		return E("PORTAL_PROTOCOL", "门户文档缺少标识、标题或正文。")
	}
	*d = PortalDocument{ID: wire.ID, Title: wire.Title, Version: wire.Version, PublishedAt: wire.PublishedAt, Body: normalizePortalBody(body)}
	return nil
}

// Normalize legacy escape sequences in prose after the JSON decoder has
// already interpreted its own escapes. Keep inline/fenced code and escaped
// backslashes intact; never run a second JSON/string unquote on the body.
func normalizePortalBody(body string) string {
	body = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(body)
	var out strings.Builder
	code := ""
	for i := 0; i < len(body); {
		c := body[i]
		if c == '`' || c == '~' {
			j := i + 1
			for j < len(body) && body[j] == c {
				j++
			}
			mark := body[i:j]
			if code == mark {
				code = ""
			} else if code == "" && (c == '`' || len(mark) >= 3) {
				code = mark
			}
			out.WriteString(mark)
			i = j
			continue
		}
		if c == '\\' && code == "" {
			switch {
			case strings.HasPrefix(body[i:], `\\`):
				out.WriteString(`\\`)
				i += 2
				continue
			case strings.HasPrefix(body[i:], `\r\n`):
				out.WriteByte('\n')
				i += 4
				continue
			case strings.HasPrefix(body[i:], `\n`):
				out.WriteByte('\n')
				i += 2
				continue
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

type PortalNewsItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Excerpt     string `json:"excerpt"`
	PublishedAt string `json:"publishedAt"`
	Read        bool   `json:"read"`
	Seen        bool   `json:"seen"`
}
type PortalNews struct {
	Items       []PortalNewsItem `json:"items"`
	NextCursor  string           `json:"nextCursor"`
	UnreadCount *int             `json:"unreadCount"`
	UnseenCount *int             `json:"unseenCount"`
}

func (n *PortalNews) UnmarshalJSON(raw []byte) error {
	type wire PortalNews
	var parsed wire
	if e := json.Unmarshal(raw, &parsed); e != nil {
		return e
	}
	if parsed.Items == nil {
		return E("PORTAL_PROTOCOL", "门户动态列表缺少 items。")
	}
	for _, item := range parsed.Items {
		if item.ID == "" || item.Title == "" {
			return E("PORTAL_PROTOCOL", "门户动态缺少标识或标题。")
		}
	}
	*n = PortalNews(parsed)
	return nil
}
