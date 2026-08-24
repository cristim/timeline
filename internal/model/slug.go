package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Slug policy (DM-2a): lowercase ASCII kebab-case, deterministic collision
// resolution, permanent once published.

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ligatures maps letters that NFD decomposition cannot reduce to ASCII.
var ligatures = strings.NewReplacer(
	"ß", "ss", "ẞ", "SS", "æ", "ae", "Æ", "AE", "œ", "oe", "Œ", "OE",
	"ø", "o", "Ø", "O", "đ", "d", "Đ", "D", "ł", "l", "Ł", "L",
	"þ", "th", "Þ", "TH", "ð", "d", "Ð", "D",
)

// Slugify derives the base slug from an English name.
func Slugify(name string) string {
	// Unicode-transliterate: ligature table, then decompose and drop
	// combining marks (é -> e).
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	ascii, _, err := transform.String(t, ligatures.Replace(name))
	if err != nil {
		ascii = name
	}
	s := strings.ToLower(ascii)
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}

// AssignSlugs resolves collisions deterministically (DM-2a): the highest-
// importance claimant keeps the bare slug (ties broken by seed id); the rest
// append start year, then type, then wikidata id. Duplicate seed ids or an
// unresolvable collision are errors, not silent renames.
func AssignSlugs(entities []*Entity) error {
	seen := map[string]bool{}
	for _, e := range entities {
		if e.SeedID == "" {
			return fmt.Errorf("entity %q has empty seed id", e.Name)
		}
		if seen[e.SeedID] {
			return fmt.Errorf("duplicate seed id %q", e.SeedID)
		}
		seen[e.SeedID] = true
	}

	byBase := map[string][]*Entity{}
	for _, e := range entities {
		base := Slugify(e.Name)
		if base == "" {
			return fmt.Errorf("entity %q slugifies to empty", e.SeedID)
		}
		byBase[base] = append(byBase[base], e)
	}
	taken := map[string]*Entity{}
	bases := make([]string, 0, len(byBase))
	for b := range byBase {
		bases = append(bases, b)
	}
	sort.Strings(bases)
	for _, base := range bases {
		group := byBase[base]
		sort.Slice(group, func(i, j int) bool {
			if group[i].Importance != group[j].Importance {
				return group[i].Importance > group[j].Importance
			}
			return group[i].SeedID < group[j].SeedID
		})
		for rank, e := range group {
			if rank == 0 {
				e.Slug = base
			} else {
				e.Slug = disambiguate(base, e, taken)
			}
			if e.Slug == "" || taken[e.Slug] != nil {
				return fmt.Errorf("unresolvable slug collision on %q (seed id %q)", base, e.SeedID)
			}
			taken[e.Slug] = e
		}
	}
	return nil
}

func disambiguate(base string, e *Entity, taken map[string]*Entity) string {
	year := int(SecondsToYear(e.T0))
	candidates := []string{
		fmt.Sprintf("%s-%d", base, year),
		fmt.Sprintf("%s-%s", base, e.Type),
	}
	if e.Wikidata != "" {
		candidates = append(candidates, fmt.Sprintf("%s-%s", base, strings.ToLower(e.Wikidata)))
	}
	for _, c := range candidates {
		if taken[c] == nil {
			return c
		}
	}
	return ""
}
