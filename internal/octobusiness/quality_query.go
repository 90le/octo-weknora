package octobusiness

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
)

type qualityFilter struct {
	LibraryID, Keyword, Outcome, Topic, Group string
	From, To                                  time.Time
	Page, PageSize                            int
}

type qualityReview struct {
	TenantID          uint64
	ChannelID         string
	ScopeID           string
	MessageID         string
	LibraryID         string
	State             string
	Suggestion        string
	ModelID           string
	Finding           string
	EvidenceSignature string
	AttemptCount      int
	AvailableAt       *time.Time
	Disposition       string
	Topic             string
	GroupID           string
	IssueID           string
	UpdatedAt         time.Time
}

func (qualityReview) TableName() string { return "octo_quality_reviews" }

type qualityReviewView struct {
	State       string `json:"state"`
	Suggestion  string `json:"suggestion"`
	Reason      string `json:"reason"`
	Disposition string `json:"disposition"`
	Topic       string `json:"topic"`
	IssueID     string `json:"issue_id"`
	LibraryID   string `json:"library_id"`
}

func (review qualityReview) view() qualityReviewView {
	var finding qualityFinding
	_ = json.Unmarshal([]byte(review.Finding), &finding)
	return qualityReviewView{
		State: review.State, Suggestion: review.Suggestion, Reason: limitQualityText(finding.Reason, 180),
		Disposition: review.Disposition, Topic: review.Topic, IssueID: review.IssueID,
		LibraryID: review.LibraryID,
	}
}

func (s *Service) qualityReviewQuery(ctx context.Context, scope *qualityScope) *gorm.DB {
	return s.db.WithContext(ctx).Model(&qualityReview{}).
		Where("tenant_id = ? AND channel_id = ? AND scope_id = ?", scope.TenantID, scope.ChannelID, scope.ID)
}

func qualityGroupKey(scope *qualityScope, row qualityRow) string {
	libraries := slices.Clone(row.libraries)
	sort.Strings(libraries)
	return digest(scope.ID, scope.ChannelID, qualityQuestionKey(row.Question), strings.Join(libraries, ","))
}

type qualityStatistics struct {
	Questions   int             `json:"questions"`
	Users       int             `json:"users"`
	Reviewed    int             `json:"reviewed"`
	NeedsReview int             `json:"needs_review"`
	Unavailable int             `json:"unavailable"`
	Failed      int             `json:"failed"`
	Groups      []qualityGroup  `json:"groups"`
	Days        []qualityBucket `json:"days"`
	Topics      []qualityBucket `json:"topics"`
	Similar     []qualityPair   `json:"similar"`
}

type qualityBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type qualityGroup struct {
	ID        string `json:"id"`
	Question  string `json:"question"`
	Count     int    `json:"count"`
	Users     int    `json:"users"`
	Gaps      int    `json:"gaps"`
	Example   string `json:"example_id"`
	people    map[string]bool
	libraries []string
}

type qualityPair struct {
	A qualityGroup `json:"a"`
	B qualityGroup `json:"b"`
}

func qualityBigrams(s string) map[string]bool {
	runes := make([]rune, 0, len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			runes = append(runes, r)
		}
	}
	out := map[string]bool{}
	for i := 1; i < len(runes); i++ {
		out[string(runes[i-1:i+1])] = true
	}
	return out
}

func similarQualityGroups(groups []qualityGroup) []qualityPair {
	// A candidate is advisory. Preserve distinct questions until a maintainer
	// compares their wording and selects a canonical group.
	pairs := []qualityPair{}
	features := make([]map[string]bool, len(groups))
	for i, group := range groups {
		features[i] = qualityBigrams(group.Question)
	}
	for i, a := range groups {
		seen := features[i]
		if len(seen) < 3 {
			continue
		}
		for j := i + 1; j < len(groups) && len(pairs) < 12; j++ {
			b := groups[j]
			if !sameQualityLibraries(a.libraries, b.libraries) {
				continue
			}
			shared := 0
			for feature := range features[j] {
				if seen[feature] {
					shared++
				}
			}
			if shared >= 3 && shared*100 >= (len(seen)+len(features[j])-shared)*55 {
				pairs = append(pairs, qualityPair{A: a, B: b})
			}
		}
	}
	return pairs
}

func qualityBuckets(counts map[string]int, chronologic bool) []qualityBucket {
	rows := make([]qualityBucket, 0, len(counts))
	for key, value := range counts {
		rows = append(rows, qualityBucket{Label: key, Count: value})
	}
	sort.Slice(rows, func(i, j int) bool {
		if chronologic || rows[i].Count == rows[j].Count {
			return rows[i].Label < rows[j].Label
		}
		return rows[i].Count > rows[j].Count
	})
	return rows
}

func qualityStats(rows []qualityRow) qualityStatistics {
	stats := qualityStatistics{Questions: len(rows), Groups: []qualityGroup{}}
	people := map[string]bool{}
	topics, days := map[string]int{}, map[string]int{}
	groups := map[string]*qualityGroup{}
	for _, row := range rows {
		people[row.userID] = true
		day := row.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
		days[day]++
		group := groups[row.GroupKey]
		if group == nil {
			group = &qualityGroup{
				ID: row.GroupKey, Question: limitQualityText(row.Question, 150),
				Example: row.ID, libraries: row.libraries, people: map[string]bool{},
			}
			groups[row.GroupKey] = group
		}
		group.Count++
		group.people[row.userID] = true
		if row.Outcome == "suspected_gap" {
			group.Gaps++
		}
		if row.Outcome == "failed" || row.Outcome == "retrieval_warning" {
			stats.Failed++
		}
		if row.UnavailableSources > 0 {
			stats.Unavailable++
		}
		if qualityMatches(row, "attention") {
			stats.NeedsReview++
		}
		if row.Review != nil {
			if row.Review.State == "done" {
				stats.Reviewed++
			}
			if row.Review.Topic != "" {
				topics[row.Review.Topic]++
			}
		}
	}
	for _, group := range groups {
		group.Users = len(group.people)
		stats.Groups = append(stats.Groups, *group)
	}
	sort.Slice(stats.Groups, func(i, j int) bool {
		a, b := stats.Groups[i], stats.Groups[j]
		if a.Count == b.Count {
			return a.ID < b.ID
		}
		return a.Count > b.Count
	})
	stats.Users = len(people)
	stats.Topics, stats.Days = qualityBuckets(topics, false), qualityBuckets(days, true)
	stats.Groups = stats.Groups[:min(len(stats.Groups), 80)]
	stats.Similar = similarQualityGroups(stats.Groups[:min(len(stats.Groups), 48)])
	return stats
}

func qualityMatches(row qualityRow, outcome string) bool {
	review := qualityReviewView{}
	if row.Review != nil {
		review = *row.Review
	}
	switch outcome {
	case "attention":
		if review.Disposition != "" || review.IssueID != "" {
			return false
		}
		return review.Suggestion == "process" || review.State == "failed" ||
			(row.Outcome == "suspected_gap" && row.Repeats > 1) ||
			row.UnavailableSources > 0 || row.Outcome == "failed" || row.Outcome == "retrieval_warning"
	case "repeated":
		return row.Outcome == "suspected_gap" && row.Repeats > 1
	case "sources":
		return len(row.Sources) > 0
	case "unavailable":
		return row.UnavailableSources > 0
	case "failed":
		return row.Outcome == "failed" || row.Outcome == "retrieval_warning"
	case "dismiss", "watch", "uncertain", "process":
		return review.Suggestion == outcome
	case "linked":
		return review.IssueID != ""
	case "reviewed":
		return review.State == "done"
	default:
		return true
	}
}

func (s *Service) collectQuality(
	ctx context.Context, scope *qualityScope, from, to time.Time,
) ([]qualityRow, bool, error) {
	// Narrow candidates in SQL and check the structured snapshot before exposing any rows.
	pattern := `%"ScopeID":"` + scope.ID + `"%`
	cursor, err := s.qualityInbox(ctx, scope).
		Where("created_at >= ? AND created_at < ? AND authority LIKE ?", from, to, pattern).
		Order("created_at DESC, message_id DESC").Rows()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = cursor.Close() }()
	var rows []qualityRow
	// The UI flags an incomplete window so a safety cap cannot be mistaken for a full count.
	const candidateLimit = 12000
	for count := 0; cursor.Next(); count++ {
		if count == candidateLimit {
			return rows, true, nil
		}
		var entry qualityInbox
		if err := s.db.ScanRows(cursor, &entry); err != nil {
			return nil, false, err
		}
		if row, ok := entry.row(scope); ok {
			row.GroupKey = qualityGroupKey(scope, row)
			rows = append(rows, row)
		}
	}
	return rows, false, cursor.Err()
}

func (s *Service) enrichQuality(ctx context.Context, scope *qualityScope, rows []qualityRow) error {
	for i := 0; i < len(rows); i += 80 {
		window := rows[i:min(i+80, len(rows))]
		ids := make([]string, 0, len(window))
		for _, row := range window {
			ids = append(ids, row.ID)
		}
		var reviews []qualityReview
		if err := s.qualityReviewQuery(ctx, scope).Where("message_id IN ?", ids).Find(&reviews).Error; err != nil {
			return err
		}
		byID := make(map[string]qualityReview, len(reviews))
		for _, review := range reviews {
			byID[review.MessageID] = review
		}
		for j := range window {
			if review, ok := byID[window[j].ID]; ok && (review.LibraryID == "" || scope.permits(review.LibraryID)) {
				view := review.view()
				window[j].Review = &view
				if review.GroupID != "" {
					window[j].GroupKey = review.GroupID
				}
			}
		}
		if err := s.markUnavailableQualitySources(ctx, scope.TenantID, window); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) queryQuality(ctx context.Context, scopeID string, f qualityFilter) (*qualityPage, error) {
	scope, err := s.qualityScope(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	if f.From.IsZero() {
		f.From = f.To.AddDate(0, 0, -14)
	}
	if !f.From.Before(f.To) || f.To.After(f.From.AddDate(0, 0, 93)) || len(f.Keyword) > 600 ||
		f.Page > 10000 || f.LibraryID != "" && !scope.permits(f.LibraryID) {
		return nil, ErrInvalid
	}
	f.Page = max(1, f.Page)
	if f.PageSize <= 0 {
		f.PageSize = 20
	}
	f.PageSize = min(f.PageSize, 100)
	rows, truncated, err := s.collectQuality(ctx, scope, f.From, f.To)
	if err != nil {
		return nil, err
	}
	if err := s.enrichQuality(ctx, scope, rows); err != nil {
		return nil, err
	}
	matches := rows[:0]
	for _, row := range rows {
		if f.LibraryID != "" && !contains(row.libraries, f.LibraryID) {
			continue
		}
		matches = append(matches, row)
	}
	counts := map[string]int{}
	for _, row := range matches {
		if row.Outcome == "suspected_gap" {
			counts[row.GroupKey]++
		}
	}
	for i := range matches {
		matches[i].Repeats = counts[matches[i].GroupKey]
	}
	page := &qualityPage{
		Rows: []qualityRow{}, Libraries: scope.Libraries, Since: f.From, Until: f.To,
		Truncated: truncated, SampleSize: len(matches),
	}
	keyword := strings.ToLower(strings.TrimSpace(f.Keyword))
	filtered := make([]qualityRow, 0, len(matches))
	for _, row := range matches {
		if !qualityMatches(row, f.Outcome) || f.Group != "" && f.Group != row.GroupKey ||
			f.Topic != "" && (row.Review == nil || row.Review.Topic != f.Topic) ||
			keyword != "" && !strings.Contains(strings.ToLower(row.Question+"\n"+row.Answer), keyword) {
			continue
		}
		filtered = append(filtered, row)
	}
	page.Total, page.Stats = len(filtered), qualityStats(filtered)
	start := min(len(filtered), (f.Page-1)*f.PageSize)
	page.Rows = append(page.Rows, filtered[start:min(start+f.PageSize, len(filtered))]...)
	return page, nil
}
