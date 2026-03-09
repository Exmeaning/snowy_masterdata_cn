package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// ── 输出数据结构 ─────────────────────────────────────────────

// MoeExtraPart 个别角色拥有的特殊部位（如 unique_head）
type MoeExtraPart struct {
	CharacterID int            `json:"characterId"`
	PartType    string         `json:"partType"`
	Variants    []PartVariant  `json:"variants"`
}

// MoeCostumeEntry 以 costumeNumber（groupId / 1000）为主键
type MoeCostumeEntry struct {
	CostumeNumber int                      `json:"costumeNumber"`
	Name          string                   `json:"name"`
	Costume3DType string                   `json:"costume3dType"`
	Rarity        string                   `json:"costume3dRarity"`
	Designer      string                   `json:"designer"`
	PartTypes     []string                 `json:"partTypes"`
	CharacterIDs  []int                    `json:"characterIds"`
	Gender        string                   `json:"gender"`
	Parts         map[string][]PartVariant `json:"parts"`
	ExtraParts    []MoeExtraPart           `json:"extraParts,omitempty"`
	Source        string                   `json:"source"`
	CardIDs       []int                    `json:"cardIds,omitempty"`
	ShopInfo      *ShopInfo                `json:"shopInfo,omitempty"`
	PublishedAt   *int64                   `json:"publishedAt,omitempty"`
	ArchiveAt     *int64                   `json:"archivePublishedAt,omitempty"`
}

// MoeDefaultEntry 默认服装（groupId < 1000）
type MoeDefaultEntry struct {
	GroupID     int    `json:"groupId"`
	Name        string `json:"name"`
	CharacterID int    `json:"characterId"`
	PartType    string `json:"partType"`
	ColorID     int    `json:"colorId"`
	ColorName   string `json:"colorName"`
	Asset       string `json:"assetbundleName"`
	Rarity      string `json:"costume3dRarity"`
	Type        string `json:"costume3dType"`
}

type MoeOutputStats struct {
	Total         int            `json:"total"`
	TotalDefaults int            `json:"totalDefaults"`
	BySource      map[string]int `json:"by_source"`
	ByPartType    map[string]int `json:"by_partType"`
	ByGender      map[string]int `json:"by_gender"`
	ByRarity      map[string]int `json:"by_rarity"`
}

type MoePreprocessorOutput struct {
	Stats    MoeOutputStats    `json:"stats"`
	Costumes []MoeCostumeEntry `json:"costumes"`
	Defaults []MoeDefaultEntry `json:"defaults"`
}

// ── 核心逻辑 ─────────────────────────────────────────────────

func RunMoePreprocessor(repoDir string) error {
	masterDir := filepath.Join(repoDir, "master")
	log.Println("[MoePreprocessor] Loading master data...")

	costume3ds, err := loadJSON[[]Costume3D](filepath.Join(masterDir, "costume3ds.json"))
	if err != nil {
		return fmt.Errorf("costume3ds.json: %w", err)
	}
	cardCostume3ds, err := loadJSON[[]CardCostume3D](filepath.Join(masterDir, "cardCostume3ds.json"))
	if err != nil {
		return fmt.Errorf("cardCostume3ds.json: %w", err)
	}
	shopItems, err := loadJSON[[]Costume3DShopItem](filepath.Join(masterDir, "costume3dShopItems.json"))
	if err != nil {
		return fmt.Errorf("costume3dShopItems.json: %w", err)
	}

	costumeByID := make(map[int]Costume3D)
	for _, c := range *costume3ds {
		costumeByID[c.ID] = c
	}

	// 分离默认 / 非默认，按 costumeNumber 分组
	numberGroup := make(map[int][]Costume3D)
	var defaultRecords []Costume3D
	for _, c := range *costume3ds {
		if c.Costume3DGroupID == nil {
			continue
		}
		gid := *c.Costume3DGroupID
		if gid < 1000 {
			defaultRecords = append(defaultRecords, c)
		} else {
			numberGroup[gid/1000] = append(numberGroup[gid/1000], c)
		}
	}

	// card 关联
	cardByNumber := make(map[int]map[int]bool)
	for _, e := range *cardCostume3ds {
		c, ok := costumeByID[e.Costume3dID]
		if !ok || c.Costume3DGroupID == nil || *c.Costume3DGroupID < 1000 {
			continue
		}
		num := *c.Costume3DGroupID / 1000
		if cardByNumber[num] == nil {
			cardByNumber[num] = make(map[int]bool)
		}
		cardByNumber[num][e.CardID] = true
	}

	// shop 关联
	shopByNumber := make(map[int]Costume3DShopItem)
	for _, item := range *shopItems {
		for _, ptr := range []*int{item.HeadCostume3dID, item.BodyCostume3dID} {
			if ptr == nil {
				continue
			}
			c, ok := costumeByID[*ptr]
			if !ok || c.Costume3DGroupID == nil || *c.Costume3DGroupID < 1000 {
				continue
			}
			num := *c.Costume3DGroupID / 1000
			if _, exists := shopByNumber[num]; !exists {
				shopByNumber[num] = item
			}
		}
	}

	// 聚合
	costumes := make([]MoeCostumeEntry, 0, len(numberGroup))
	for num, group := range numberGroup {
		entry := buildMoeCostumeEntry(num, group)
		if cards, ok := cardByNumber[num]; ok && len(cards) > 0 {
			entry.Source = "card"
			entry.CardIDs = sortedKeys(cards)
		} else if si, ok := shopByNumber[num]; ok {
			entry.Source = "shop"
			entry.ShopInfo = &ShopInfo{
				ShopItemID: si.ID, ShopGroupID: si.GroupID,
				Costs: si.Costs, StartAt: si.StartAt,
			}
		} else {
			entry.Source = "other"
		}
		costumes = append(costumes, entry)
	}
	sort.Slice(costumes, func(i, j int) bool {
		return costumes[i].CostumeNumber < costumes[j].CostumeNumber
	})

	// 默认服装
	defaults := buildDefaults(defaultRecords)

	// 统计
	stats := MoeOutputStats{
		Total: len(costumes), TotalDefaults: len(defaults),
		BySource: make(map[string]int), ByPartType: make(map[string]int),
		ByGender: make(map[string]int), ByRarity: make(map[string]int),
	}
	for _, item := range costumes {
		stats.BySource[item.Source]++
		for _, pt := range item.PartTypes {
			stats.ByPartType[pt]++
		}
		stats.ByGender[item.Gender]++
		stats.ByRarity[item.Rarity]++
	}

	output := MoePreprocessorOutput{Stats: stats, Costumes: costumes, Defaults: defaults}
	outPath := filepath.Join(masterDir, "moe_costume.json")
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	log.Printf("[MoePreprocessor] ✅ %s (%d costumes + %d defaults, %.1f KB)",
		outPath, stats.Total, stats.TotalDefaults, float64(len(data))/1024)
	log.Printf("[MoePreprocessor]    source=%v gender=%v rarity=%v",
		stats.BySource, stats.ByGender, stats.ByRarity)
	return nil
}

// buildMoeCostumeEntry 聚合同一 costumeNumber 的所有记录
// 只保留一份 parts 模板（多数派），个别角色的特殊部位记入 extraParts
func buildMoeCostumeEntry(costumeNumber int, group []Costume3D) MoeCostumeEntry {
	// 按 characterId 分组
	charRecords := make(map[int][]Costume3D)
	for _, c := range group {
		charRecords[c.CharacterID] = append(charRecords[c.CharacterID], c)
	}

	// 为每个角色构建 parts 签名，找出多数派作为模板
	type partsSig struct {
		parts map[string][]PartVariant
		key   string // 用于比较的序列化 key
	}

	charSigs := make(map[int]partsSig)
	for charID, records := range charRecords {
		parts := buildPartsMap(records)
		charSigs[charID] = partsSig{parts: parts, key: partsKey(parts)}
	}

	// 统计签名频率，找多数派
	sigCount := make(map[string]int)
	for _, sig := range charSigs {
		sigCount[sig.key]++
	}
	bestKey := ""
	bestCount := 0
	for k, cnt := range sigCount {
		if cnt > bestCount {
			bestCount = cnt
			bestKey = k
		}
	}

	// 取多数派的 parts 作为模板
	var templateParts map[string][]PartVariant
	for _, sig := range charSigs {
		if sig.key == bestKey {
			templateParts = sig.parts
			break
		}
	}

	// 收集 extraParts：与模板不同的角色的额外部位
	var extraParts []MoeExtraPart
	templatePtSet := make(map[string]bool)
	for pt := range templateParts {
		templatePtSet[pt] = true
	}

	charIDs := make([]int, 0, len(charRecords))
	for id := range charRecords {
		charIDs = append(charIDs, id)
	}
	sort.Ints(charIDs)

	for _, charID := range charIDs {
		sig := charSigs[charID]
		for pt, variants := range sig.parts {
			if !templatePtSet[pt] {
				extraParts = append(extraParts, MoeExtraPart{
					CharacterID: charID,
					PartType:    pt,
					Variants:    variants,
				})
			}
		}
	}
	sort.Slice(extraParts, func(i, j int) bool {
		if extraParts[i].CharacterID != extraParts[j].CharacterID {
			return extraParts[i].CharacterID < extraParts[j].CharacterID
		}
		return extraParts[i].PartType < extraParts[j].PartType
	})

	// 元数据
	rep := group[0]
	for _, c := range group {
		if c.Name != "" && c.Name != "未設定" {
			rep = c
			break
		}
	}

	gender := "female"
	allMale := true
	for _, id := range charIDs {
		if !maleCharacterIDs[id] {
			allMale = false
			break
		}
	}
	if allMale && len(charIDs) > 0 {
		gender = "male"
	}

	allPartTypes := make(map[string]bool)
	for pt := range templateParts {
		allPartTypes[pt] = true
	}
	for _, ep := range extraParts {
		allPartTypes[ep.PartType] = true
	}
	partTypes := make([]string, 0, len(allPartTypes))
	for pt := range allPartTypes {
		partTypes = append(partTypes, pt)
	}
	sort.Strings(partTypes)

	rarity := rep.Costume3DRarity
	if rarity == "" {
		rarity = "normal"
	}
	cosType := rep.Costume3DType
	if cosType == "" {
		cosType = "normal"
	}

	return MoeCostumeEntry{
		CostumeNumber: costumeNumber,
		Name:          rep.Name,
		Costume3DType: cosType,
		Rarity:        rarity,
		Designer:      rep.Designer,
		PartTypes:     partTypes,
		CharacterIDs:  charIDs,
		Gender:        gender,
		Parts:         templateParts,
		ExtraParts:    extraParts,
		PublishedAt:   rep.PublishedAt,
		ArchiveAt:     rep.ArchivePublishedAt,
	}
}

// buildPartsMap 从一组记录构建 partType → []PartVariant
func buildPartsMap(records []Costume3D) map[string][]PartVariant {
	sort.Slice(records, func(i, j int) bool {
		ci, cj := records[i].ColorID, records[j].ColorID
		if ci == 0 { ci = 1 }
		if cj == 0 { cj = 1 }
		if ci != cj { return ci < cj }
		return records[i].ID < records[j].ID
	})

	parts := make(map[string][]PartVariant)
	seen := make(map[string]map[string]bool)
	for _, c := range records {
		if seen[c.PartType] == nil {
			seen[c.PartType] = make(map[string]bool)
		}
		if !seen[c.PartType][c.AssetbundleName] {
			seen[c.PartType][c.AssetbundleName] = true
			cid := c.ColorID
			if cid == 0 { cid = 1 }
			parts[c.PartType] = append(parts[c.PartType], PartVariant{
				ColorID: cid, ColorName: c.ColorName,
				AssetbundleName: c.AssetbundleName,
			})
		}
	}
	return parts
}

// partsKey 生成 parts 的可比较字符串
func partsKey(parts map[string][]PartVariant) string {
	keys := make([]string, 0, len(parts))
	for pt := range parts {
		keys = append(keys, pt)
	}
	sort.Strings(keys)

	var b []byte
	for _, pt := range keys {
		b = append(b, pt...)
		b = append(b, ':')
		for _, v := range parts[pt] {
			b = append(b, fmt.Sprintf("%d,%s,%s;", v.ColorID, v.ColorName, v.AssetbundleName)...)
		}
		b = append(b, '|')
	}
	return string(b)
}

func buildDefaults(records []Costume3D) []MoeDefaultEntry {
	defaults := make([]MoeDefaultEntry, 0, len(records))
	for _, c := range records {
		rarity := c.Costume3DRarity
		if rarity == "" { rarity = "normal" }
		ctype := c.Costume3DType
		if ctype == "" { ctype = "normal" }
		cid := c.ColorID
		if cid == 0 { cid = 1 }
		defaults = append(defaults, MoeDefaultEntry{
			GroupID: *c.Costume3DGroupID, Name: c.Name,
			CharacterID: c.CharacterID, PartType: c.PartType,
			ColorID: cid, ColorName: c.ColorName,
			Asset: c.AssetbundleName, Rarity: rarity, Type: ctype,
		})
	}
	sort.Slice(defaults, func(i, j int) bool {
		if defaults[i].GroupID != defaults[j].GroupID {
			return defaults[i].GroupID < defaults[j].GroupID
		}
		return defaults[i].CharacterID < defaults[j].CharacterID
	})
	return defaults
}
