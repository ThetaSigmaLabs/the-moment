// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// validateSourceURL checks that rawURL is a well-formed http or https URL.
// Used to prevent SSRF via user-supplied source URLs.
func validateSourceURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("source URL must use http or https")
	}
	return nil
}

// ─── Structs ──────────────────────────────────────────────────────────────────

// OpenPrintTagSource is one row in openprinttag_sources — a configured external
// filament data source for the OpenPrintTag lookup workflow.
type OpenPrintTagSource struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	SourceType string `json:"source_type"` // "ofd_api" | "filament_db_api"
	Enabled    bool   `json:"enabled"`
	IsDefault  bool   `json:"is_default"`
}

// OPTSearchResult is a normalised filament record returned by any source adapter.
// For OFD sources, SourceRef is a compound path:
//
//	"brands/{brand}/materials/{material}/filaments/{filament}"
//	"brands/{brand}/materials/{material}/filaments/{filament}/variants/{variant}"
//
// The variant form is set once the user picks a colour; the filament form is set
// on initial search. FilamentName holds the product-line name (e.g. "PolyLite PLA");
// ColorName holds the specific colour once a variant is selected.
type OPTSearchResult struct {
	SourceID       int     `json:"source_id"`
	SourceName     string  `json:"source_name"`
	SourceRef      string  `json:"source_ref"`
	FilamentName   string  `json:"filament_name"`   // e.g. "PolyLite PLA"
	MaterialClass  string  `json:"material_class"`  // "FFF" | "SLA" (from OFD material_class)
	Brand          string  `json:"brand"`
	Material       string  `json:"material"`
	ColorName      string  `json:"color_name"`
	ColorHex       string  `json:"color_hex"`
	DiameterMM     float64 `json:"diameter_mm"`
	Density        float64 `json:"density"`
	DefaultWeightG float64 `json:"default_weight_g"`
	MinPrintTemp   int     `json:"min_print_temp"`
	MaxPrintTemp   int     `json:"max_print_temp"`
	MinBedTemp     int     `json:"min_bed_temp"`
	MaxBedTemp     int     `json:"max_bed_temp"`
}

// OPTFilamentVariant is one colour option returned by the /api/openprinttag/variants endpoint.
type OPTFilamentVariant struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	ColorHex string `json:"color_hex"` // without # prefix
}

// OPTVariantResult bundles colour variants with shared filament parameters.
type OPTVariantResult struct {
	Variants     []OPTFilamentVariant
	MinPrintTemp int
	MaxPrintTemp int
	MinBedTemp   int
	MaxBedTemp   int
	Density      float64
	DiameterMM   float64
}

// OPTAdapter is the interface every filament-database source adapter must satisfy.
// Register new adapters with RegisterOPTAdapter in an init() call so they can
// be discovered without modifying core dispatch code.
type OPTAdapter interface {
	// SourceType returns the source_type key stored in openprinttag_sources.
	SourceType() string
	// TestURL returns the URL to probe for connectivity testing.
	TestURL(baseURL string) string
	// Search returns filaments matching query from the given source.
	Search(source OpenPrintTagSource, query string) ([]OPTSearchResult, error)
	// FetchByRef re-fetches a specific filament by its adapter-specific ref string.
	FetchByRef(source OpenPrintTagSource, ref string) (*OPTSearchResult, error)
	// Variants returns colour/size options for a filament ref plus shared parameters.
	// Returns nil, nil for adapters that do not support multi-variant selection.
	Variants(source OpenPrintTagSource, ref string) (*OPTVariantResult, error)
}

// optAdapters is the global adapter registry keyed by source_type.
var optAdapters = map[string]OPTAdapter{}

// RegisterOPTAdapter registers an adapter. Call from init() to enable plug-in discovery.
func RegisterOPTAdapter(a OPTAdapter) {
	optAdapters[a.SourceType()] = a
}

// validOPTSourceType returns true when sourceType has a registered adapter.
func validOPTSourceType(sourceType string) bool {
	_, ok := optAdapters[sourceType]
	return ok
}

// defaultOPTSources is the seed list restored by ResetOPTSourcesToDefaults.
var defaultOPTSources = []OpenPrintTagSource{
	{
		Name:       "Open Filament Database",
		URL:        "https://api.openfilamentdatabase.org",
		SourceType: "ofd_api",
		Enabled:    true,
		IsDefault:  true,
	},
	{
		// filament-db is a self-hosted community project (github.com/hyiger/filament-db).
		// Update this URL to point to your own instance (default port: 3000). No public instance exists.
		Name:       "filament-db (self-hosted)",
		URL:        "http://localhost:3000",
		SourceType: "filament_db_api",
		Enabled:    false,
		IsDefault:  false,
	},
}

// ─── DB migration ────────────────────────────────────────────────────────────

func (b *FilamentBridge) migrateOpenPrintTagSources() error {
	_, err := b.db.Exec(`CREATE TABLE IF NOT EXISTS openprinttag_sources (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		url         TEXT NOT NULL,
		source_type TEXT NOT NULL,
		enabled     INTEGER NOT NULL DEFAULT 1,
		is_default  INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("create openprinttag_sources: %w", err)
	}

	// Remove the CHECK constraint on source_type if present (pre-adapter-registry schema).
	var tableSQL string
	_ = b.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='openprinttag_sources'`).Scan(&tableSQL)
	if strings.Contains(tableSQL, "CHECK") {
		_, _ = b.db.Exec(`CREATE TABLE openprinttag_sources_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL,
			url         TEXT NOT NULL,
			source_type TEXT NOT NULL,
			enabled     INTEGER NOT NULL DEFAULT 1,
			is_default  INTEGER NOT NULL DEFAULT 0,
			created_at  DATETIME DEFAULT (datetime('now'))
		)`)
		_, _ = b.db.Exec(`INSERT INTO openprinttag_sources_new SELECT * FROM openprinttag_sources`)
		_, _ = b.db.Exec(`DROP TABLE openprinttag_sources`)
		_, _ = b.db.Exec(`ALTER TABLE openprinttag_sources_new RENAME TO openprinttag_sources`)
	}

	var count int
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM openprinttag_sources`).Scan(&count); err != nil {
		return fmt.Errorf("count openprinttag_sources: %w", err)
	}
	if count == 0 {
		// Fresh install: seed all defaults.
		for _, s := range defaultOPTSources {
			if _, err := b.db.Exec(
				`INSERT INTO openprinttag_sources (name, url, source_type, enabled, is_default) VALUES (?,?,?,?,?)`,
				s.Name, s.URL, s.SourceType, boolToInt(s.Enabled), boolToInt(s.IsDefault),
			); err != nil {
				return fmt.Errorf("seed openprinttag_sources: %w", err)
			}
		}
	} else {
		// Existing install: idempotently add any default source type not yet present.
		for _, s := range defaultOPTSources {
			var exists int
			_ = b.db.QueryRow(
				`SELECT COUNT(*) FROM openprinttag_sources WHERE source_type=?`, s.SourceType,
			).Scan(&exists)
			if exists == 0 {
				_, _ = b.db.Exec(
					`INSERT INTO openprinttag_sources (name, url, source_type, enabled, is_default) VALUES (?,?,?,?,?)`,
					s.Name, s.URL, s.SourceType, boolToInt(s.Enabled), boolToInt(s.IsDefault),
				)
			}
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ─── DB CRUD ─────────────────────────────────────────────────────────────────

func (b *FilamentBridge) ListOPTSources() ([]OpenPrintTagSource, error) {
	rows, err := b.db.Query(
		`SELECT id, name, url, source_type, enabled, is_default FROM openprinttag_sources ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenPrintTagSource
	for rows.Next() {
		var s OpenPrintTagSource
		var enabled, isDefault int
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.SourceType, &enabled, &isDefault); err != nil {
			return nil, err
		}
		s.Enabled = enabled != 0
		s.IsDefault = isDefault != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

func (b *FilamentBridge) InsertOPTSource(s OpenPrintTagSource) (int, error) {
	res, err := b.db.Exec(
		`INSERT INTO openprinttag_sources (name, url, source_type, enabled, is_default) VALUES (?,?,?,?,0)`,
		s.Name, s.URL, s.SourceType, boolToInt(s.Enabled),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func (b *FilamentBridge) UpdateOPTSource(s OpenPrintTagSource) error {
	_, err := b.db.Exec(
		`UPDATE openprinttag_sources SET name=?, url=?, source_type=?, enabled=? WHERE id=?`,
		s.Name, s.URL, s.SourceType, boolToInt(s.Enabled), s.ID,
	)
	return err
}

func (b *FilamentBridge) DeleteOPTSource(id int) error {
	_, err := b.db.Exec(`DELETE FROM openprinttag_sources WHERE id=?`, id)
	return err
}

func (b *FilamentBridge) ResetOPTSourcesToDefaults() error {
	if _, err := b.db.Exec(`DELETE FROM openprinttag_sources`); err != nil {
		return err
	}
	for _, s := range defaultOPTSources {
		if _, err := b.db.Exec(
			`INSERT INTO openprinttag_sources (name, url, source_type, enabled, is_default) VALUES (?,?,?,?,?)`,
			s.Name, s.URL, s.SourceType, boolToInt(s.Enabled), boolToInt(s.IsDefault),
		); err != nil {
			return err
		}
	}
	return nil
}

// ─── Connectivity test ────────────────────────────────────────────────────────

// TestOPTSource sends a lightweight probe to the source URL and returns latency in ms.
func TestOPTSource(source OpenPrintTagSource) (latencyMS int, err error) {
	testURL := buildTestURL(source)
	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Get(testURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	elapsed := int(time.Since(start).Milliseconds())
	if resp.StatusCode >= 400 {
		return elapsed, fmt.Errorf("HTTP %d from %s", resp.StatusCode, testURL)
	}
	return elapsed, nil
}

func buildTestURL(source OpenPrintTagSource) string {
	a, ok := optAdapters[source.SourceType]
	if !ok {
		return strings.TrimRight(source.URL, "/")
	}
	return a.TestURL(source.URL)
}

// ─── Search dispatch ──────────────────────────────────────────────────────────

// SearchOPTSource queries the external source and returns normalised results.
func SearchOPTSource(source OpenPrintTagSource, query string) ([]OPTSearchResult, error) {
	a, ok := optAdapters[source.SourceType]
	if !ok {
		return nil, fmt.Errorf("unsupported source type %q", source.SourceType)
	}
	return a.Search(source, query)
}

// ─── OFD hierarchy types ──────────────────────────────────────────────────────

// These types mirror the static JSON structure of api.openfilamentdatabase.org/api/v1/.
// The OFD API is purely static files — there is no search endpoint.
// Search is performed by traversing: brands index → brand detail → material detail.
// Temperatures and colour variants live in the filament detail level.

// ofdBrandsIndex is the top-level wrapper returned by /api/v1/brands/index.json.
// The API returns an object {version, generated_at, count, brands:[...]},
// NOT a bare array.
type ofdBrandsIndex struct {
	Brands []ofdBrandIndexEntry `json:"brands"`
}

type ofdBrandIndexEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	MaterialCount int    `json:"material_count"`
}

type ofdBrandDetail struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Slug      string           `json:"slug"`
	Materials []ofdMaterialEntry `json:"materials"`
}

type ofdMaterialEntry struct {
	ID            string `json:"id"`
	Material      string `json:"material"` // "PLA", "PETG", "ABS" …
	Slug          string `json:"slug"`
	FilamentCount int    `json:"filament_count"`
}

type ofdMaterialDetail struct {
	ID            string             `json:"id"`
	Material      string             `json:"material"`
	Slug          string             `json:"slug"`
	MaterialClass string             `json:"material_class"` // "FFF" | "SLA"
	Filaments     []ofdFilamentEntry `json:"filaments"`
}

type ofdFilamentEntry struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	VariantCount int    `json:"variant_count"`
}

type ofdFilamentDetail struct {
	ID                  string           `json:"id"`
	Name                string           `json:"name"`
	DiameterTolerance   float64          `json:"diameter_tolerance"`
	Density             float64          `json:"density"`
	MinPrintTemperature int              `json:"min_print_temperature"`
	MaxPrintTemperature int              `json:"max_print_temperature"`
	MinBedTemperature   int              `json:"min_bed_temperature"`
	MaxBedTemperature   int              `json:"max_bed_temperature"`
	Variants            []ofdVariantEntry `json:"variants"`
}

type ofdVariantEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ColorHex  string `json:"color_hex"`
	Slug      string `json:"slug"`
	SizeCount int    `json:"size_count"`
}

// ─── OFD in-process brands cache (1-hour TTL) ────────────────────────────────

var (
	ofdBrandsCacheMu  sync.RWMutex
	ofdBrandsCache    []ofdBrandIndexEntry
	ofdBrandsCacheExp time.Time
)

// resetOFDBrandsCache clears the in-process OFD brands cache. Used in tests.
func resetOFDBrandsCache() {
	ofdBrandsCacheMu.Lock()
	ofdBrandsCache = nil
	ofdBrandsCacheExp = time.Time{}
	ofdBrandsCacheMu.Unlock()
}

func ofdGetBrands(base string) ([]ofdBrandIndexEntry, error) {
	ofdBrandsCacheMu.RLock()
	if len(ofdBrandsCache) > 0 && time.Now().Before(ofdBrandsCacheExp) {
		cp := make([]ofdBrandIndexEntry, len(ofdBrandsCache))
		copy(cp, ofdBrandsCache)
		ofdBrandsCacheMu.RUnlock()
		return cp, nil
	}
	ofdBrandsCacheMu.RUnlock()

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/api/v1/brands/index.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brands index HTTP %d", resp.StatusCode)
	}
	var wrapper ofdBrandsIndex
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("brands index decode: %w", err)
	}
	brands := wrapper.Brands

	ofdBrandsCacheMu.Lock()
	ofdBrandsCache = brands
	ofdBrandsCacheExp = time.Now().Add(time.Hour)
	ofdBrandsCacheMu.Unlock()
	return brands, nil
}

func ofdGetBrandDetail(base, brandSlug string) (*ofdBrandDetail, error) {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(
		base + "/api/v1/brands/" + url.PathEscape(brandSlug) + "/index.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brand detail HTTP %d", resp.StatusCode)
	}
	var d ofdBrandDetail
	return &d, json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&d)
}

func ofdGetMaterialDetail(base, brandSlug, materialSlug string) (*ofdMaterialDetail, error) {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(
		base + "/api/v1/brands/" + url.PathEscape(brandSlug) +
			"/materials/" + url.PathEscape(materialSlug) + "/index.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("material detail HTTP %d", resp.StatusCode)
	}
	var d ofdMaterialDetail
	return &d, json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&d)
}

func ofdGetFilamentDetail(base, brandSlug, materialSlug, filamentSlug string) (*ofdFilamentDetail, error) {
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(
		base + "/api/v1/brands/" + url.PathEscape(brandSlug) +
			"/materials/" + url.PathEscape(materialSlug) +
			"/filaments/" + url.PathEscape(filamentSlug) + "/index.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("filament detail HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var d ofdFilamentDetail
	return &d, json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&d)
}

// ─── OFD search ──────────────────────────────────────────────────────────────

// ofdSearch queries the Open Filament Database by traversing its static JSON hierarchy:
// brands index → matching brand detail (materials) → matching material detail (filaments).
//
// Results are filament-level (no colours yet). The caller fetches colour variants
// separately via /api/openprinttag/variants after the user selects a filament.
//
// SourceRef format: "brands/{brand_slug}/materials/{material_slug}/filaments/{filament_slug}"
func ofdSearch(source OpenPrintTagSource, query string) ([]OPTSearchResult, error) {
	if err := validateSourceURL(source.URL); err != nil {
		return nil, fmt.Errorf("ofd source: %w", err)
	}
	base := strings.TrimRight(source.URL, "/")
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}

	// Step 1: brands index (1-hour in-process cache)
	allBrands, err := ofdGetBrands(base)
	if err != nil {
		return nil, fmt.Errorf("ofd brands index: %w", err)
	}

	// Match brands by name or slug substring
	var matched []ofdBrandIndexEntry
	for _, b := range allBrands {
		bName := strings.ToLower(b.Name)
		bSlug := strings.ToLower(b.Slug)
		for _, w := range words {
			if strings.Contains(bName, w) || strings.Contains(bSlug, w) {
				matched = append(matched, b)
				break
			}
		}
	}
	// Fallback: no brand word matched — use top 3 brands by material count
	if len(matched) == 0 {
		top := make([]ofdBrandIndexEntry, len(allBrands))
		copy(top, allBrands)
		sort.Slice(top, func(i, j int) bool { return top[i].MaterialCount > top[j].MaterialCount })
		if len(top) > 3 {
			top = top[:3]
		}
		matched = top
	} else if len(matched) > 3 {
		matched = matched[:3]
	}

	var out []OPTSearchResult
	for _, brand := range matched {
		brandDetail, err := ofdGetBrandDetail(base, brand.Slug)
		if err != nil {
			continue
		}
		brandName := brandDetail.Name
		if brandName == "" {
			brandName = brand.Name
		}

		// Match materials by name or slug substring
		var matMatched []ofdMaterialEntry
		for _, m := range brandDetail.Materials {
			mLow := strings.ToLower(m.Material)
			mSlug := strings.ToLower(m.Slug)
			for _, w := range words {
				if strings.Contains(mLow, w) || strings.Contains(mSlug, w) {
					matMatched = append(matMatched, m)
					break
				}
			}
		}
		// Fallback: no material word — take first 2 materials
		if len(matMatched) == 0 {
			matMatched = brandDetail.Materials
		}
		if len(matMatched) > 2 {
			matMatched = matMatched[:2]
		}

		for _, mat := range matMatched {
			matDetail, err := ofdGetMaterialDetail(base, brand.Slug, mat.Slug)
			if err != nil {
				continue
			}

			filaments := matDetail.Filaments
			if len(filaments) > 10 {
				filaments = filaments[:10]
			}

			for _, f := range filaments {
				ref := "brands/" + brand.Slug + "/materials/" + mat.Slug + "/filaments/" + f.Slug
				out = append(out, OPTSearchResult{
					SourceID:      source.ID,
					SourceName:    source.Name,
					SourceRef:     ref,
					FilamentName:  f.Name,
					MaterialClass: matDetail.MaterialClass,
					Brand:         brandName,
					Material:      mat.Material,
				})
			}
		}
	}

	return out, nil
}

// ofdFetchVariants fetches colour variants and print parameters for a specific OFD filament.
// filamentRef format: "brands/{brand}/materials/{material}/filaments/{filament}"
func ofdFetchVariants(source OpenPrintTagSource, filamentRef string) ([]OPTFilamentVariant, *ofdFilamentDetail, error) {
	base := strings.TrimRight(source.URL, "/")
	parts := strings.Split(filamentRef, "/")
	if len(parts) != 6 || parts[0] != "brands" || parts[2] != "materials" || parts[4] != "filaments" {
		return nil, nil, fmt.Errorf("invalid OFD filament ref: %s", filamentRef)
	}
	detail, err := ofdGetFilamentDetail(base, parts[1], parts[3], parts[5])
	if err != nil {
		return nil, nil, err
	}
	variants := make([]OPTFilamentVariant, 0, len(detail.Variants))
	for _, v := range detail.Variants {
		variants = append(variants, OPTFilamentVariant{
			Slug:     v.Slug,
			Name:     v.Name,
			ColorHex: strings.TrimPrefix(v.ColorHex, "#"),
		})
	}
	return variants, detail, nil
}

// ofdFetchByRef re-fetches a filament from OFD using a compound path ref.
// Accepts two ref formats:
//
//	"brands/{b}/materials/{m}/filaments/{f}"              — uses first variant colour
//	"brands/{b}/materials/{m}/filaments/{f}/variants/{v}" — uses the specific colour
func ofdFetchByRef(source OpenPrintTagSource, ref string) (*OPTSearchResult, error) {
	base := strings.TrimRight(source.URL, "/")
	parts := strings.Split(ref, "/")
	if len(parts) < 6 || parts[0] != "brands" || parts[2] != "materials" || parts[4] != "filaments" {
		return nil, fmt.Errorf("invalid OFD ref format: %s", ref)
	}
	brandSlug := parts[1]
	materialSlug := parts[3]
	filamentSlug := parts[5]
	var variantSlug string
	if len(parts) == 8 && parts[6] == "variants" {
		variantSlug = parts[7]
	}

	// Brand name from cache (best-effort; fall back to slug)
	var brandName string
	if brands, err := ofdGetBrands(base); err == nil {
		for _, b := range brands {
			if b.Slug == brandSlug {
				brandName = b.Name
				break
			}
		}
	}
	if brandName == "" {
		brandName = brandSlug
	}

	detail, err := ofdGetFilamentDetail(base, brandSlug, materialSlug, filamentSlug)
	if err != nil {
		return nil, fmt.Errorf("ofd filament detail: %w", err)
	}

	var colorName, colorHex string
	if variantSlug != "" {
		for _, v := range detail.Variants {
			if v.Slug == variantSlug {
				colorName = v.Name
				colorHex = strings.TrimPrefix(v.ColorHex, "#")
				break
			}
		}
	} else if len(detail.Variants) > 0 {
		colorName = detail.Variants[0].Name
		colorHex = strings.TrimPrefix(detail.Variants[0].ColorHex, "#")
	}

	// OFD stores diameter_tolerance, not nominal diameter.
	// Default to 1.75 mm (most common FFF diameter).
	const defaultDiameterMM = 1.75

	minPrint := detail.MinPrintTemperature
	maxPrint := detail.MaxPrintTemperature
	minBed := detail.MinBedTemperature
	maxBed := detail.MaxBedTemperature
	density := detail.Density

	if minPrint == 0 && maxPrint == 0 {
		if tt := tigerTagLookupTemps(materialSlug); tt != nil {
			minPrint = tt.MinPrintTemp
			maxPrint = tt.MaxPrintTemp
			minBed = tt.MinBedTemp
			maxBed = tt.MaxBedTemp
			if density == 0 {
				density = tt.Density
			}
		}
	}

	return &OPTSearchResult{
		SourceID:     source.ID,
		SourceName:   source.Name,
		SourceRef:    ref,
		FilamentName: detail.Name,
		Brand:        brandName,
		Material:     materialSlug,
		ColorName:    colorName,
		ColorHex:     colorHex,
		DiameterMM:   defaultDiameterMM,
		Density:      density,
		MinPrintTemp: minPrint,
		MaxPrintTemp: maxPrint,
		MinBedTemp:   minBed,
		MaxBedTemp:   maxBed,
	}, nil
}

// ─── filament-db adapter ──────────────────────────────────────────────────────
//
// filament-db is an open-source self-hosted filament management application:
// https://github.com/hyiger/filament-db — default port: 3456.
// No public hosted instance exists; users configure their own URL.
//
// API reference (OpenAPI 3.0 spec at /api-docs on a running instance):
//
//	GET /api/filaments?search={query}&type={type}&vendor={vendor}
//	  → []filamentDBFilament   (array; search is case-insensitive regex on name)
//
//	GET /api/filaments/{id}
//	  → filamentDBFilament     (single full record, includes diameter)
//
//	GET /api/health             (used for connectivity test; returns 200 OK)
//
// SourceRef format for filament_db_api:
//
//	"{_id}"  — the MongoDB _id string of the filament document
//
// Note: the list endpoint returns filament summaries; diameter is only present
// in the detail response (GET /api/filaments/:id). DiameterMM is 0 for search
// results and populated when the user picks a specific filament via FetchByRef.

// filamentDBFilament is the JSON shape returned by both GET /api/filaments and
// GET /api/filaments/:id in hyiger/filament-db.
type filamentDBFilament struct {
	ID                string   `json:"_id"`
	Name              string   `json:"name"`
	Vendor            string   `json:"vendor"`
	Type              string   `json:"type"`   // "PLA", "PETG", "ABS" …
	Color             string   `json:"color"`  // hex without #, or "" when none
	SecondaryColors   []string `json:"secondaryColors"`
	Diameter          float64  `json:"diameter"`
	Density           float64  `json:"density"`
	NetFilamentWeight float64  `json:"netFilamentWeight"`
	Temperatures      struct {
		Nozzle float64 `json:"nozzle"`
		Bed    float64 `json:"bed"`
	} `json:"temperatures"`
}

// filamentDBToOPT maps a filamentDBFilament to an OPTSearchResult.
// filament-db provides a single nozzle/bed temperature (not a range), so
// both Min and Max are set to the same value.
func filamentDBToOPT(source OpenPrintTagSource, e filamentDBFilament) OPTSearchResult {
	name := e.Name
	if name == "" {
		name = e.Vendor + " " + e.Type
	}
	return OPTSearchResult{
		SourceID:       source.ID,
		SourceName:     source.Name,
		SourceRef:      e.ID,
		FilamentName:   name,
		Brand:          e.Vendor,
		Material:       e.Type,
		ColorHex:       strings.TrimPrefix(e.Color, "#"),
		DiameterMM:     e.Diameter,
		Density:        e.Density,
		DefaultWeightG: e.NetFilamentWeight,
		MinPrintTemp:   int(e.Temperatures.Nozzle),
		MaxPrintTemp:   int(e.Temperatures.Nozzle),
		MinBedTemp:     int(e.Temperatures.Bed),
		MaxBedTemp:     int(e.Temperatures.Bed),
	}
}

// filamentDBSearch queries a filament-db-compatible REST API.
// See https://github.com/hyiger/filament-db. Configure the source URL to
// point at your own instance (default port 3456).
func filamentDBSearch(source OpenPrintTagSource, query string) ([]OPTSearchResult, error) {
	if err := validateSourceURL(source.URL); err != nil {
		return nil, fmt.Errorf("filament-db source: %w", err)
	}
	base := strings.TrimRight(source.URL, "/")
	searchURL := fmt.Sprintf("%s/api/filaments?search=%s", base, url.QueryEscape(query))

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("filament-db search request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("filament-db search HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw []filamentDBFilament
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("filament-db search decode: %w", err)
	}

	out := make([]OPTSearchResult, 0, len(raw))
	for _, e := range raw {
		out = append(out, filamentDBToOPT(source, e))
	}
	return out, nil
}

// ─── Fetch single record by ref ───────────────────────────────────────────────

// fetchOPTByRef re-fetches a single filament from an external source using its source_ref.
func fetchOPTByRef(source OpenPrintTagSource, ref string) (*OPTSearchResult, error) {
	a, ok := optAdapters[source.SourceType]
	if !ok {
		return nil, fmt.Errorf("unsupported source type %q", source.SourceType)
	}
	return a.FetchByRef(source, ref)
}

// fetchOPTVariants returns colour/size variants for a filament ref using the registered adapter.
func fetchOPTVariants(source OpenPrintTagSource, ref string) (*OPTVariantResult, error) {
	a, ok := optAdapters[source.SourceType]
	if !ok {
		return nil, fmt.Errorf("unsupported source type %q", source.SourceType)
	}
	return a.Variants(source, ref)
}

func filamentDBFetchByID(source OpenPrintTagSource, id string) (*OPTSearchResult, error) {
	base := strings.TrimRight(source.URL, "/")
	detailURL := fmt.Sprintf("%s/api/filaments/%s", base, url.PathEscape(id))
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(detailURL)
	if err != nil {
		return nil, fmt.Errorf("filament-db detail request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("filament-db detail HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var e filamentDBFilament
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&e); err != nil {
		return nil, fmt.Errorf("filament-db detail decode: %w", err)
	}
	r := filamentDBToOPT(source, e)
	return &r, nil
}

// ─── Adapter structs ──────────────────────────────────────────────────────────

type ofdAdapter struct{}

func (ofdAdapter) SourceType() string { return "ofd_api" }
func (ofdAdapter) TestURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/api/v1/brands/index.json"
}
func (ofdAdapter) Search(source OpenPrintTagSource, query string) ([]OPTSearchResult, error) {
	return ofdSearch(source, query)
}
func (ofdAdapter) FetchByRef(source OpenPrintTagSource, ref string) (*OPTSearchResult, error) {
	return ofdFetchByRef(source, ref)
}
func (ofdAdapter) Variants(source OpenPrintTagSource, ref string) (*OPTVariantResult, error) {
	variants, detail, err := ofdFetchVariants(source, ref)
	if err != nil {
		return nil, err
	}
	return &OPTVariantResult{
		Variants:     variants,
		MinPrintTemp: detail.MinPrintTemperature,
		MaxPrintTemp: detail.MaxPrintTemperature,
		MinBedTemp:   detail.MinBedTemperature,
		MaxBedTemp:   detail.MaxBedTemperature,
		Density:      detail.Density,
		DiameterMM:   1.75,
	}, nil
}

type filamentDBAdapter struct{}

func (filamentDBAdapter) SourceType() string { return "filament_db_api" }
func (filamentDBAdapter) TestURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/api/health"
}
func (filamentDBAdapter) Search(source OpenPrintTagSource, query string) ([]OPTSearchResult, error) {
	return filamentDBSearch(source, query)
}
func (filamentDBAdapter) FetchByRef(source OpenPrintTagSource, ref string) (*OPTSearchResult, error) {
	return filamentDBFetchByID(source, ref)
}
func (filamentDBAdapter) Variants(_ OpenPrintTagSource, _ string) (*OPTVariantResult, error) {
	return nil, nil
}

func init() {
	RegisterOPTAdapter(ofdAdapter{})
	RegisterOPTAdapter(filamentDBAdapter{})
}

// ─── Spoolman helpers ─────────────────────────────────────────────────────────

// CreateSpoolmanFilamentFromOPT creates a new Spoolman filament from an OPT search result,
// including all nfc_* extra fields. Returns the new filament ID.
func (b *FilamentBridge) CreateSpoolmanFilamentFromOPT(result OPTSearchResult) (int, error) {
	data := map[string]interface{}{
		"material": result.Material,
		"diameter": result.DiameterMM,
	}
	if result.DefaultWeightG > 0 {
		data["weight"] = result.DefaultWeightG
	}
	if result.Density > 0 {
		data["density"] = result.Density
	}
	hex := strings.TrimPrefix(result.ColorHex, "#")
	if hex != "" {
		data["color_hex"] = hex
	}
	// FilamentName is the product-line name (e.g. "PolyLite PLA").
	// Fall back to ColorName, Material, or a placeholder.
	name := result.FilamentName
	if name == "" {
		name = result.ColorName
	}
	if name == "" {
		name = result.Material
	}
	if name == "" {
		name = "NFC Filament"
	}
	data["name"] = name

	if result.Brand != "" {
		if v, err := b.spoolman.FindVendorByName(result.Brand); err == nil && v != nil {
			data["vendor_id"] = v.ID
		}
	}

	created, err := b.spoolman.CreateFilament(data)
	if err != nil {
		return 0, fmt.Errorf("create Spoolman filament: %w", err)
	}

	b.writeOPTNFCFields(created.ID, result)
	return created.ID, nil
}

// UpdateSpoolmanFilamentNFCFields writes nfc_* extra fields to an existing Spoolman filament.
// Also updates standard filament fields (material, colour, diameter, density, weight).
func (b *FilamentBridge) UpdateSpoolmanFilamentNFCFields(filamentID int, result OPTSearchResult) error {
	update := map[string]interface{}{}
	if result.Material != "" {
		update["material"] = result.Material
	}
	hex := strings.TrimPrefix(result.ColorHex, "#")
	if hex != "" {
		update["color_hex"] = hex
	}
	if result.DiameterMM > 0 {
		update["diameter"] = result.DiameterMM
	}
	if result.Density > 0 {
		update["density"] = result.Density
	}
	if result.DefaultWeightG > 0 {
		update["weight"] = result.DefaultWeightG
	}
	if result.MinPrintTemp > 0 {
		update["settings_extruder_temp"] = result.MinPrintTemp
	}
	if len(update) > 0 {
		if err := b.spoolman.UpdateFilament(filamentID, update); err != nil {
			log.Printf("warning: UpdateSpoolmanFilamentNFCFields: updating standard fields: %v", err)
		}
	}

	b.writeOPTNFCFields(filamentID, result)
	return nil
}

// writeOPTNFCFields writes nfc_* extra fields for a filament. Errors are logged, not returned,
// because missing extra fields don't break the tag creation flow.
func (b *FilamentBridge) writeOPTNFCFields(filamentID int, result OPTSearchResult) {
	fields := map[string]string{
		"nfc_min_print_temp": strconv.Itoa(result.MinPrintTemp),
		"nfc_max_print_temp": strconv.Itoa(result.MaxPrintTemp),
		"nfc_min_bed_temp":   strconv.Itoa(result.MinBedTemp),
		"nfc_max_bed_temp":   strconv.Itoa(result.MaxBedTemp),
	}
	if result.Density > 0 {
		fields["nfc_transmission_distance"] = fmt.Sprintf("%g", result.Density)
	}
	if result.MaterialClass != "" {
		fields["nfc_material_class"] = result.MaterialClass
	}
	for k, v := range fields {
		if err := b.spoolman.SetFilamentExtraField(filamentID, k, v); err != nil {
			log.Printf("warning: writeOPTNFCFields: set %s on filament %d: %v", k, filamentID, err)
		}
	}
}

// ─── TigerTag material catalog (temperature fallback for OFD) ─────────────────
//
// TigerTag exposes a public material catalog at api.tigertag.io with density and
// recommended temperatures. Used only when OFD returns zero temperatures — never
// blocks tag creation if unreachable. Cache TTL is 24 hours.

// tigerTagMaterial mirrors one entry from api.tigertag.io/api:tigertag/material/get/all.
type tigerTagMaterial struct {
	ID           int     `json:"id"`
	Label        string  `json:"label"`
	MaterialType string  `json:"material_type"`
	Density      float64 `json:"density"`
	Recommended  struct {
		NozzleTempMin int `json:"nozzleTempMin"`
		NozzleTempMax int `json:"nozzleTempMax"`
		BedTempMin    int `json:"bedTempMin"`
		BedTempMax    int `json:"bedTempMax"`
	} `json:"recommended"`
}

type tigerTagTemps struct {
	MinPrintTemp int
	MaxPrintTemp int
	MinBedTemp   int
	MaxBedTemp   int
	Density      float64
}

var (
	tigerTagCacheMu  sync.RWMutex
	tigerTagCache    map[string][]tigerTagMaterial // keyed by baseURL
	tigerTagCacheExp map[string]time.Time
)

// resetTigerTagCache clears cache for tests.
func resetTigerTagCache() {
	tigerTagCacheMu.Lock()
	tigerTagCache = nil
	tigerTagCacheExp = nil
	tigerTagCacheMu.Unlock()
}

func tigerTagGetMaterials(baseURL string) ([]tigerTagMaterial, error) {
	tigerTagCacheMu.RLock()
	if tigerTagCache != nil {
		if exp, ok := tigerTagCacheExp[baseURL]; ok && time.Now().Before(exp) {
			cp := make([]tigerTagMaterial, len(tigerTagCache[baseURL]))
			copy(cp, tigerTagCache[baseURL])
			tigerTagCacheMu.RUnlock()
			return cp, nil
		}
	}
	tigerTagCacheMu.RUnlock()

	fetchURL := strings.TrimRight(baseURL, "/") + "/material/get/all"
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(fetchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tigertag HTTP %d", resp.StatusCode)
	}
	var materials []tigerTagMaterial
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&materials); err != nil {
		return nil, fmt.Errorf("tigertag decode: %w", err)
	}

	tigerTagCacheMu.Lock()
	if tigerTagCache == nil {
		tigerTagCache = make(map[string][]tigerTagMaterial)
		tigerTagCacheExp = make(map[string]time.Time)
	}
	tigerTagCache[baseURL] = materials
	tigerTagCacheExp[baseURL] = time.Now().Add(24 * time.Hour)
	tigerTagCacheMu.Unlock()
	return materials, nil
}

// tigerTagLookupTempsFrom looks up temperatures from a specific TigerTag base URL.
// Priority: exact label match > label-prefix match > material_type substring.
// Returns nil if no match or all recommended temps are zero.
func tigerTagLookupTempsFrom(baseURL, label string) *tigerTagTemps {
	materials, err := tigerTagGetMaterials(baseURL)
	if err != nil {
		return nil
	}
	labelLow := strings.ToLower(label)

	var best *tigerTagMaterial
	var bestPriority int // 3=exact, 2=prefix, 1=material_type substr

	for i := range materials {
		m := &materials[i]
		mLow := strings.ToLower(m.Label)
		mtLow := strings.ToLower(m.MaterialType)
		var priority int
		switch {
		case mLow == labelLow:
			priority = 3
		case strings.HasPrefix(mLow, labelLow):
			priority = 2
		case mtLow != "" && strings.Contains(mtLow, labelLow):
			priority = 1
		}
		if priority > bestPriority {
			bestPriority = priority
			best = m
		}
	}

	if best == nil {
		return nil
	}
	r := best.Recommended
	if r.NozzleTempMin == 0 && r.NozzleTempMax == 0 && r.BedTempMin == 0 && r.BedTempMax == 0 {
		return nil
	}
	return &tigerTagTemps{
		MinPrintTemp: r.NozzleTempMin,
		MaxPrintTemp: r.NozzleTempMax,
		MinBedTemp:   r.BedTempMin,
		MaxBedTemp:   r.BedTempMax,
		Density:      best.Density,
	}
}

// tigerTagLookupTemps looks up temperatures from the public TigerTag API.
func tigerTagLookupTemps(label string) *tigerTagTemps {
	return tigerTagLookupTempsFrom("https://api.tigertag.io/api:tigertag", label)
}
