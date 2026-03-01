package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var maleCharacterIDs = map[int]bool{
	12: true, 13: true, 16: true, 23: true, 26: true,
}

var (
	cosGroupRe    = regexp.MustCompile(`^(cos\d+)_`)
	colorSuffixRe = regexp.MustCompile(`_(\d{2})$`)
)

// ── 数据结构 ──────────────────────────────────────────────────

type Costume3D struct {
	ID                 int    `json:"id"`
	Costume3DGroupID   *int   `json:"costume3dGroupId,omitempty"`
	Costume3DType      string `json:"costume3dType,omitempty"`
	Name               string `json:"name"`
	PartType           string `json:"partType"`
	ColorID            int    `json:"colorId,omitempty"`
	ColorName          string `json:"colorName,omitempty"`
	AssetbundleName    string `json:"assetbundleName"`
	CharacterID        int    `json:"characterId"`
	Costume3DRarity    string `json:"costume3dRarity,omitempty"`
	Designer           string `json:"designer,omitempty"`
	PublishedAt        *int64 `json:"publishedAt,omitempty"`
	ArchivePublishedAt *int64 `json:"archivePublishedAt,omitempty"`
}

type CardCostume3D struct {
	CardID      int `json:"cardId"`
	Costume3dID int `json:"costume3dId"`
}

type ShopCost struct {
	ResourceType string `json:"resourceType,omitempty"`
	ResourceID   int    `json:"resourceId,omitempty"`
	Quantity     int    `json:"quantity,omitempty"`
}

type Costume3DShopItem struct {
	ID              int        `json:"id"`
	GroupID         *int       `json:"groupId,omitempty"`
	HeadCostume3dID *int       `json:"headCostume3dId,omitempty"`
	BodyCostume3dID *int       `json:"bodyCostume3dId,omitempty"`
	Costs           []ShopCost `json:"costs,omitempty"`
	StartAt         *int64     `json:"startAt,omitempty"`
}

type PartVariant struct {
	ColorID         int    `json:"colorId"`
	ColorName       string `json:"colorName"`
	AssetbundleName string `json:"assetbundleName"`
}

type ShopInfo struct {
	ShopItemID  int        `json:"shopItemId"`
	ShopGroupID *int       `json:"shopGroupId,omitempty"`
	Costs       []ShopCost `json:"costs,omitempty"`
	StartAt     *int64     `json:"startAt,omitempty"`
}

type CostumeEntry struct {
	ID                 int                      `json:"id"`
	Costume3DGroupID   *int                     `json:"costume3dGroupId"`
	Costume3DType      string                   `json:"costume3dType"`
	Name               string                   `json:"name"`
	PartTypes          []string                 `json:"partTypes"`
	CharacterIDs       []int                    `json:"characterIds"`
	Gender             string                   `json:"gender"`
	Costume3DRarity    string                   `json:"costume3dRarity"`
	CostumePrefix      string                   `json:"costumePrefix"`
	Designer           string                   `json:"designer"`
	PublishedAt        *int64                   `json:"publishedAt,omitempty"`
	ArchivePublishedAt *int64                   `json:"archivePublishedAt,omitempty"`
	Parts              map[string][]PartVariant `json:"parts"`
	Source             string                   `json:"source"`
	CardIDs            []int                    `json:"cardIds,omitempty"`
	ShopInfo           *ShopInfo                `json:"shopInfo,omitempty"`
}

type OutputStats struct {
	Total      int            `json:"total"`
	BySource   map[string]int `json:"by_source"`
	ByPartType map[string]int `json:"by_partType"`
	ByGender   map[string]int `json:"by_gender"`
	ByRarity   map[string]int `json:"by_rarity"`
}

type PreprocessorOutput struct {
	Stats    OutputStats    `json:"stats"`
	Costumes []CostumeEntry `json:"costumes"`
}

// ── 核心逻辑 ──────────────────────────────────────────────────

func getCostumeGroupPrefix(assetName string) string {
	if m := cosGroupRe.FindStringSubmatch(assetName); m != nil {
		return m[1]
	}
	return colorSuffixRe.ReplaceAllString(assetName, "")
}

func buildCostumeEntry(group []Costume3D, prefix, source string) CostumeEntry {
	sort.Slice(group, func(i, j int) bool {
		ci, cj := group[i].ColorID, group[j].ColorID
		if ci == 0 {
			ci = 1
		}
		if cj == 0 {
			cj = 1
		}
		if ci != cj {
			return ci < cj
		}
		return group[i].ID < group[j].ID
	})

	rep := group[0]

	parts := make(map[string][]PartVariant)
	seen := make(map[string]map[string]bool)

	for _, c := range group {
		if seen[c.PartType] == nil {
			seen[c.PartType] = make(map[string]bool)
		}
		if !seen[c.PartType][c.AssetbundleName] {
			seen[c.PartType][c.AssetbundleName] = true
			cid := c.ColorID
			if cid == 0 {
				cid = 1
			}
			parts[c.PartType] = append(parts[c.PartType], PartVariant{
				ColorID:         cid,
				ColorName:       c.ColorName,
				AssetbundleName: c.AssetbundleName,
			})
		}
	}

	charSet := make(map[int]bool)
	for _, c := range group {
		charSet[c.CharacterID] = true
	}
	charIDs := sortedKeys(charSet)

	gender := "female"
	allMale := true
	for _, id := range charIDs {
		if !maleCharacterIDs[id] {
			allMale = false
			break
		}
	}
	if allMale {
		gender = "male"
	}

	partTypes := make([]string, 0, len(parts))
	for pt := range parts {
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

	return CostumeEntry{
		ID: rep.ID, Costume3DGroupID: rep.Costume3DGroupID,
		Costume3DType: cosType, Name: rep.Name,
		PartTypes: partTypes, CharacterIDs: charIDs,
		Gender: gender, Costume3DRarity: rarity,
		CostumePrefix: prefix, Designer: rep.Designer,
		PublishedAt: rep.PublishedAt, ArchivePublishedAt: rep.ArchivePublishedAt,
		Parts: parts, Source: source,
	}
}

func RunPreprocessor(repoDir string) error {
	masterDir := filepath.Join(repoDir, "master")

	log.Println("[Preprocessor] Loading master data...")

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

	// ── Lookup maps ───────────────────────────────────────────
	costumeByID := make(map[int]Costume3D)
	for _, c := range *costume3ds {
		costumeByID[c.ID] = c
	}

	// ── Step 1: card → costume IDs ────────────────────────────
	// (保留映射关系用于 Step 4)

	// ── Step 2: shop → costume IDs ────────────────────────────
	// (保留映射关系用于 Step 5)

	// ── Step 3: 按 prefix 分组 ────────────────────────────────
	prefixGroup := make(map[string][]Costume3D)
	costumeToPrefix := make(map[int]string)
	for _, c := range *costume3ds {
		p := getCostumeGroupPrefix(c.AssetbundleName)
		prefixGroup[p] = append(prefixGroup[p], c)
		costumeToPrefix[c.ID] = p
	}

	// ── Step 4: card-sourced ──────────────────────────────────
	processed := make(map[string]CostumeEntry)
	seenPfx := make(map[string]bool)

	cardPfxCards := make(map[string]map[int]bool)
	for _, e := range *cardCostume3ds {
		if _, ok := costumeByID[e.Costume3dID]; !ok {
			continue
		}
		p := costumeToPrefix[e.Costume3dID]
		if cardPfxCards[p] == nil {
			cardPfxCards[p] = make(map[int]bool)
		}
		cardPfxCards[p][e.CardID] = true
	}

	for pfx, cids := range cardPfxCards {
		if seenPfx[pfx] {
			continue
		}
		seenPfx[pfx] = true
		grp := prefixGroup[pfx]
		if len(grp) == 0 {
			continue
		}
		entry := buildCostumeEntry(grp, pfx, "card")
		entry.CardIDs = sortedKeys(cids)
		processed[pfx] = entry
	}

	// ── Step 5: shop-sourced ──────────────────────────────────
	shopPfxItems := make(map[string][]Costume3DShopItem)
	for _, item := range *shopItems {
		for _, ptr := range []*int{item.HeadCostume3dID, item.BodyCostume3dID} {
			if ptr == nil {
				continue
			}
			if _, ok := costumeByID[*ptr]; !ok {
				continue
			}
			p := costumeToPrefix[*ptr]
			shopPfxItems[p] = append(shopPfxItems[p], item)
		}
	}

	for pfx, items := range shopPfxItems {
		if seenPfx[pfx] {
			continue
		}
		seenPfx[pfx] = true
		grp := prefixGroup[pfx]
		if len(grp) == 0 {
			continue
		}
		si := items[0]
		entry := buildCostumeEntry(grp, pfx, "shop")
		entry.ShopInfo = &ShopInfo{
			ShopItemID: si.ID, ShopGroupID: si.GroupID,
			Costs: si.Costs, StartAt: si.StartAt,
		}
		processed[pfx] = entry
	}

	// ── Step 6: remaining ─────────────────────────────────────
	for pfx, grp := range prefixGroup {
		if seenPfx[pfx] {
			continue
		}
		seenPfx[pfx] = true
		if len(grp) == 0 {
			continue
		}
		src := "other"
		if strings.Contains(grp[0].AssetbundleName, "default") {
			src = "default"
		}
		processed[pfx] = buildCostumeEntry(grp, pfx, src)
	}

	// ── Step 7: sort ──────────────────────────────────────────
	result := make([]CostumeEntry, 0, len(processed))
	for _, e := range processed {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })

	// ── Step 8: stats ─────────────────────────────────────────
	stats := OutputStats{
		Total:    len(result),
		BySource: make(map[string]int), ByPartType: make(map[string]int),
		ByGender: make(map[string]int), ByRarity: make(map[string]int),
	}
	for _, item := range result {
		stats.BySource[item.Source]++
		for _, pt := range item.PartTypes {
			stats.ByPartType[pt]++
		}
		stats.ByGender[item.Gender]++
		stats.ByRarity[item.Costume3DRarity]++
	}

	output := PreprocessorOutput{Stats: stats, Costumes: result}

	// ── Step 9: write ─────────────────────────────────────────
	outPath := filepath.Join(masterDir, "snowy_costumes.json")
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	log.Printf("[Preprocessor] ✅ %s (%d costumes, %.1f KB)", outPath, stats.Total, float64(len(data))/1024)
	log.Printf("[Preprocessor]    source=%v gender=%v rarity=%v", stats.BySource, stats.ByGender, stats.ByRarity)
	return nil
}

func loadJSON[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &v, nil
}

func sortedKeys(m map[int]bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
