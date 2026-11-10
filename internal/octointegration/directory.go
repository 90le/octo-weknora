package octointegration

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

type Page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

type GroupEntry struct {
	GroupID      string `json:"group_id"`
	DisplayName  string `json:"display_name"`
	BotCount     int    `json:"bot_count"`
	SubareaCount int    `json:"subarea_count"`
	Pending      int    `json:"pending"`
}

type BotEntry struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Verified  bool   `json:"verified"`
	Groups    int    `json:"group_count"`
}

type AreaEntry struct {
	SubareaID   string `json:"subarea_id"`
	DisplayName string `json:"display_name"`
}

type ComparisonEntry struct {
	Bot         BotEntry `json:"bot"`
	Scope       *Scope   `json:"scope"`
	QueryCount  int      `json:"query_count"`
	ManageCount int      `json:"manage_count"`
}

type directoryQuery struct {
	q, account, group, subarea, filter string
	limit, offset                      int
}

func literalSearch(q string) string {
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(q)) + "%"
}

func pageQuery[T any](db, base *gorm.DB, p directoryQuery, order string) (Page[T], error) {
	result := Page[T]{Items: []T{}}
	if err := db.Table("(?) AS directory", base).Count(&result.Total).Error; err != nil {
		return result, err
	}
	err := db.Table("(?) AS directory", base).Order(order).Limit(p.limit).Offset(p.offset).Scan(&result.Items).Error
	return result, err
}

func (s *Store) groups(ctx context.Context, tenant uint64, p directoryQuery) (Page[GroupEntry], error) {
	db := s.db.WithContext(ctx)
	base := db.Table("octo_scopes AS s").Where("s.tenant_id = ?", tenant)
	match := db.Table("octo_scopes AS m").Select("m.group_id").Where("m.tenant_id = ?", tenant)
	if p.account != "" {
		match = match.Where("m.account_id = ? AND m.subarea_id = ''", p.account)
		base = base.Where("s.group_id IN (?)", match)
	}
	if p.q != "" {
		search := literalSearch(p.q)
		matchingNames := db.Table("octo_scopes").Select("group_id").Where("tenant_id = ?", tenant).
			Where("(LOWER(group_id) LIKE ? ESCAPE '!' OR LOWER(display_name) LIKE ? ESCAPE '!')", search, search)
		base = base.Where("s.group_id IN (?)", matchingNames)
	}
	base = base.Select(`s.group_id,
		COALESCE(MIN(CASE WHEN s.subarea_id = '' AND s.sync_status = 'verified' THEN s.display_name END),
		MIN(CASE WHEN s.subarea_id = '' THEN s.display_name END), s.group_id) AS display_name,
		COUNT(DISTINCT CASE WHEN s.subarea_id = '' THEN s.account_id END) AS bot_count,
		COUNT(DISTINCT CASE WHEN s.subarea_id <> '' THEN s.subarea_id END) AS subarea_count,
		SUM(CASE WHEN s.sync_status <> 'verified' THEN 1 ELSE 0 END) AS pending`).
		Group("s.group_id")
	switch p.filter {
	case "multiple":
		base = base.Having("COUNT(DISTINCT CASE WHEN s.subarea_id = '' THEN s.account_id END) > 1")
	case "pending":
		base = base.Having("SUM(CASE WHEN s.sync_status <> 'verified' THEN 1 ELSE 0 END) > 0")
	}
	return pageQuery[GroupEntry](db, base, p, "display_name, group_id")
}

func (s *Store) bots(ctx context.Context, tenant uint64, p directoryQuery) (Page[BotEntry], error) {
	db := s.db.WithContext(ctx)
	// Both engines can extract the saved identity's display name. This never
	// calls Octo or loads the credential to render a directory page.
	name := "json_extract(c.verified_identity, '$.name')"
	verified := "NULLIF(json_extract(c.verified_identity, '$.bot_uid'), '') IS NOT NULL"
	if db.Dialector.Name() == "postgres" {
		name = "(c.verified_identity ->> 'name')"
		verified = "NULLIF(c.verified_identity ->> 'bot_uid', '') IS NOT NULL"
	}
	base := db.Table("(SELECT account_id FROM octo_connections WHERE tenant_id = ? UNION SELECT account_id FROM octo_scopes WHERE tenant_id = ?) AS b", tenant, tenant).
		Joins("LEFT JOIN octo_connections AS c ON c.tenant_id = ? AND c.account_id = b.account_id", tenant).
		Select("b.account_id, COALESCE(NULLIF("+name+", ''), b.account_id) AS name, c.account_id IS NOT NULL AS connected, "+
			verified+" AS verified, (SELECT COUNT(*) FROM octo_scopes AS s WHERE s.tenant_id = ? AND s.account_id = b.account_id AND s.subarea_id = '') AS groups", tenant)
	if p.q != "" {
		search := literalSearch(p.q)
		base = base.Where("(LOWER(b.account_id) LIKE ? ESCAPE '!' OR LOWER("+name+") LIKE ? ESCAPE '!')", search, search)
	}
	if p.filter == "verified" {
		base = base.Where(verified)
	} else if p.filter == "pending" {
		base = base.Where("NOT (" + verified + ")")
	} else if p.filter == "connected" {
		base = base.Where("c.account_id IS NOT NULL")
	}
	return pageQuery[BotEntry](db, base, p, "name, account_id")
}

func (s *Store) areas(ctx context.Context, tenant uint64, p directoryQuery) (Page[AreaEntry], error) {
	db := s.db.WithContext(ctx)
	base := db.Table("octo_scopes").Where("tenant_id = ? AND group_id = ? AND subarea_id <> ''", tenant, p.group)
	if p.account != "" {
		base = base.Where("account_id = ?", p.account)
	}
	if p.q != "" {
		search := literalSearch(p.q)
		base = base.Where("(LOWER(display_name) LIKE ? ESCAPE '!' OR LOWER(subarea_id) LIKE ? ESCAPE '!')", search, search)
	}
	base = base.Select("subarea_id, COALESCE(MIN(CASE WHEN sync_status = 'verified' THEN display_name END), MIN(display_name)) AS display_name").Group("subarea_id")
	return pageQuery[AreaEntry](db, base, p, "display_name, subarea_id")
}

func (s *Store) compare(ctx context.Context, tenant uint64, p directoryQuery) (Page[ComparisonEntry], error) {
	db := s.db.WithContext(ctx)
	base := db.Model(&Scope{}).Where("tenant_id = ? AND group_id = ? AND subarea_id = ''", tenant, p.group)
	if p.account != "" {
		base = base.Where("account_id = ?", p.account)
	}
	parents, err := pageQuery[Scope](db, base, p, "account_id, id")
	result := Page[ComparisonEntry]{Items: []ComparisonEntry{}, Total: parents.Total}
	if err != nil || len(parents.Items) == 0 {
		return result, err
	}
	accounts, ids := []string{}, []string{}
	for _, parent := range parents.Items {
		accounts = append(accounts, parent.AccountID)
		ids = append(ids, parent.ID)
	}
	byAccount := map[string]*Scope{}
	if p.subarea == "" {
		for i := range parents.Items {
			parent := &parents.Items[i]
			byAccount[parent.AccountID] = parent
		}
	} else {
		var children []Scope
		if err := db.Where("tenant_id = ? AND group_id = ? AND subarea_id = ? AND account_id IN ?", tenant, p.group, p.subarea, accounts).Find(&children).Error; err != nil {
			return result, err
		}
		for i := range children {
			child := &children[i]
			byAccount[child.AccountID] = child
			ids = append(ids, child.ID)
		}
	}
	var connections []Connection
	if err := db.Select("account_id, verified_identity").Where("tenant_id = ? AND account_id IN ?", tenant, accounts).Find(&connections).Error; err != nil {
		return result, err
	}
	bots := map[string]BotEntry{}
	for _, conn := range connections {
		row := BotEntry{AccountID: conn.AccountID, Name: conn.AccountID, Connected: true}
		if identity := conn.trustedIdentity(); identity != nil {
			row.Verified = true
			if identity.Name != "" {
				row.Name = identity.Name
			}
		}
		bots[conn.AccountID] = row
	}
	acls, err := loadScopeACL(db, tenant, ids)
	if err != nil {
		return result, err
	}
	for _, parent := range parents.Items {
		bot, ok := bots[parent.AccountID]
		if !ok {
			bot = BotEntry{AccountID: parent.AccountID, Name: parent.AccountID}
		}
		row := ComparisonEntry{Bot: bot, Scope: byAccount[parent.AccountID]}
		if row.Scope != nil {
			local := acls[row.Scope.ID]
			row.QueryCount = len(local.QueryIDs)
			if row.Scope.InheritParent && p.subarea != "" {
				row.QueryCount = len(unionIDs(local.QueryIDs, acls[parent.ID].QueryIDs))
			}
			row.ManageCount = len(local.ManageIDs)
		}
		result.Items = append(result.Items, row)
	}
	return result, nil
}
